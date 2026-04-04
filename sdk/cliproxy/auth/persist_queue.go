package auth

import (
	"context"
	"strings"
	"sync"
)

type authPersistRequest struct {
	ctx            context.Context
	auth           *Auth
	failureMessage string
}

type authPersistQueue struct {
	manager *Manager
	wake    chan struct{}

	startOnce sync.Once
	mu        sync.Mutex
	pending   map[string]authPersistRequest
	inFlight  map[string]chan struct{}
	order     []string
	waiters   []chan struct{}
}

func newAuthPersistQueue(manager *Manager) *authPersistQueue {
	return &authPersistQueue{
		manager:  manager,
		wake:     make(chan struct{}, 1),
		pending:  make(map[string]authPersistRequest),
		inFlight: make(map[string]chan struct{}),
	}
}

func (q *authPersistQueue) enqueue(ctx context.Context, auth *Auth, failureMessage string) {
	if q == nil || q.manager == nil || auth == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return
	}
	req := authPersistRequest{
		ctx:            persistContext(ctx),
		auth:           auth.Clone(),
		failureMessage: failureMessage,
	}
	if req.failureMessage == "" {
		req.failureMessage = "failed to persist auth state"
	}

	q.mu.Lock()
	if _, exists := q.pending[authID]; !exists {
		q.order = append(q.order, authID)
	}
	q.pending[authID] = req
	q.mu.Unlock()

	q.startOnce.Do(func() {
		go q.run()
	})
	q.signal()
}

func (q *authPersistQueue) run() {
	for {
		authID, req, ok := q.pop()
		if !ok {
			<-q.wake
			continue
		}
		if err := q.manager.persistDirect(req.ctx, req.auth); err != nil {
			logEntryWithRequestID(req.ctx).WithField("auth_id", req.auth.ID).Warnf("%s: %v", req.failureMessage, err)
		}
		q.finish(authID)
	}
}

func (q *authPersistQueue) pop() (string, authPersistRequest, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.order) > 0 {
		authID := q.order[0]
		copy(q.order, q.order[1:])
		q.order[len(q.order)-1] = ""
		q.order = q.order[:len(q.order)-1]
		req, ok := q.pending[authID]
		if !ok {
			continue
		}
		delete(q.pending, authID)
		q.inFlight[authID] = make(chan struct{})
		return authID, req, true
	}
	return "", authPersistRequest{}, false
}

func (q *authPersistQueue) finish(authID string) {
	q.mu.Lock()
	done := q.inFlight[authID]
	delete(q.inFlight, authID)
	q.notifyWaitersLocked()
	q.mu.Unlock()
	if done != nil {
		close(done)
	}
}

func (q *authPersistQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *authPersistQueue) drainAuth(ctx context.Context, authID string) error {
	if q == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		q.mu.Lock()
		if q.discardPendingLocked(authID) {
			q.notifyWaitersLocked()
		}
		done := q.inFlight[authID]
		q.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (q *authPersistQueue) flush(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		q.mu.Lock()
		if len(q.pending) == 0 && len(q.inFlight) == 0 {
			q.mu.Unlock()
			return nil
		}
		waiter := make(chan struct{})
		q.waiters = append(q.waiters, waiter)
		q.mu.Unlock()

		select {
		case <-waiter:
		case <-ctx.Done():
			q.removeWaiter(waiter)
			return ctx.Err()
		}
	}
}

func (q *authPersistQueue) removeWaiter(waiter chan struct{}) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := 0; i < len(q.waiters); i++ {
		if q.waiters[i] != waiter {
			continue
		}
		copy(q.waiters[i:], q.waiters[i+1:])
		q.waiters[len(q.waiters)-1] = nil
		q.waiters = q.waiters[:len(q.waiters)-1]
		return
	}
}

func (q *authPersistQueue) notifyWaitersLocked() {
	if len(q.waiters) == 0 {
		return
	}
	waiters := q.waiters
	q.waiters = nil
	for _, waiter := range waiters {
		close(waiter)
	}
}

func (q *authPersistQueue) discardPendingLocked(authID string) bool {
	if _, ok := q.pending[authID]; !ok {
		return false
	}
	delete(q.pending, authID)
	for i := 0; i < len(q.order); {
		if q.order[i] != authID {
			i++
			continue
		}
		copy(q.order[i:], q.order[i+1:])
		q.order[len(q.order)-1] = ""
		q.order = q.order[:len(q.order)-1]
	}
	return true
}

func persistContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

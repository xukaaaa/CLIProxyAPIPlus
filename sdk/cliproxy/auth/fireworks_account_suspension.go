package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// SuspendFireworksAccountModels suspends a model for every Fireworks auth that
// shares the same account_id as the triggering auth. For a specific modelID the
// triggering auth is skipped because the caller has already suspended it. When
// modelID is empty, all models for every matching account auth (including the
// triggering auth) are suspended.
// It also marks the matching model states as suspended so the scheduler avoids
// picking sibling keys until the suspension is cleared, persists the modified
// auths, and returns the IDs of the auths that were suspended.
func SuspendFireworksAccountModels(ctx context.Context, m *Manager, reg *registry.ModelRegistry, auth *Auth, modelID, reason string) []string {
	if m == nil || reg == nil || auth == nil {
		return nil
	}
	accountID := fireworksAccountIDFromAuth(auth)
	if accountID == "" {
		return nil
	}
	modelID = strings.TrimSpace(modelID)

	// Collect matching auth IDs first to avoid holding m.mu while calling the
	// registry (the registry may acquire its own lock; ReconcileRegistryModelStates
	// takes r.mutex then m.mu, so we must not hold m.mu during registry calls).
	m.mu.RLock()
	ids := make([]string, 0, len(m.auths))
	for _, a := range m.auths {
		if a == nil {
			continue
		}
		if modelID != "" && a.ID == auth.ID {
			continue
		}
		if strings.ToLower(strings.TrimSpace(a.Provider)) != "fireworks" {
			continue
		}
		if fireworksAccountIDFromAuth(a) != accountID {
			continue
		}
		ids = append(ids, a.ID)
	}
	m.mu.RUnlock()

	// Suspend models in the registry outside the manager lock.
	modelsToSuspend := make(map[string][]string, len(ids))
	for _, id := range ids {
		if modelID == "" {
			models := reg.GetModelsForClient(id)
			if len(models) == 0 {
				continue
			}
			modelIDs := make([]string, 0, len(models))
			for _, mi := range models {
				if mi == nil {
					continue
				}
				reg.SuspendClientModel(id, mi.ID, reason)
				modelIDs = append(modelIDs, mi.ID)
			}
			modelsToSuspend[id] = modelIDs
		} else {
			reg.SuspendClientModel(id, modelID, reason)
			modelsToSuspend[id] = []string{modelID}
		}
	}

	// Update auth model states and collect snapshots under the write lock.
	m.mu.Lock()
	now := time.Now()
	suspended := make([]string, 0, len(ids))
	toPersist := make([]*Auth, 0)
	for _, id := range ids {
		a := m.auths[id]
		if a == nil {
			continue
		}
		for _, mid := range modelsToSuspend[id] {
			markModelStateSuspended(a, mid, now)
		}
		updateAggregatedAvailability(a, now)
		a.Status = StatusError
		a.UpdatedAt = now
		suspended = append(suspended, id)
		toPersist = append(toPersist, a.Clone())
	}
	m.mu.Unlock()

	for _, snapshot := range toPersist {
		m.persistAsync(ctx, snapshot, "failed to persist suspended sibling auth")
	}
	return suspended
}

func markModelStateSuspended(auth *Auth, modelID string, now time.Time) {
	state := ensureModelState(auth, modelID)
	state.Unavailable = true
	state.Status = StatusError
	state.StatusMessage = "account_suspended"
	state.NextRetryAfter = now.Add(30 * 24 * time.Hour)
	state.LastError = &Error{HTTPStatus: http.StatusPreconditionFailed, Message: "Account suspended"}
	state.UpdatedAt = now
}

func fireworksAccountIDFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if v, ok := auth.Attributes["account_id"]; ok {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	if auth.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(stringMetadataValueForAccountID(auth.Metadata["account_id"]))
}

func stringMetadataValueForAccountID(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprint(v)
}

package auth

import (
	"reflect"
	"sort"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestSuspendFireworksAccountModels_SuspendsMatchingAuths(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{
		ID:       "fw-1",
		Provider: "fireworks",
		Metadata: map[string]any{"account_id": "acc-1"},
	}
	auth2 := &Auth{
		ID:       "fw-2",
		Provider: "fireworks",
		Metadata: map[string]any{"account_id": "acc-1"},
	}
	auth3 := &Auth{
		ID:       "fw-3",
		Provider: "fireworks",
		Metadata: map[string]any{"account_id": "acc-2"},
	}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}
	if _, err := m.Register(nil, auth3); err != nil {
		t.Fatalf("register auth3: %v", err)
	}

	model := []*registry.ModelInfo{{ID: "m1"}}
	reg.RegisterClient("fw-1", "fireworks", model)
	reg.RegisterClient("fw-2", "fireworks", model)
	reg.RegisterClient("fw-3", "fireworks", model)
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
		reg.UnregisterClient("fw-3")
	})

	suspended := SuspendFireworksAccountModels(nil, m, reg, auth1, "m1", "account_suspended")
	sort.Strings(suspended)

	want := []string{"fw-2"}
	if !reflect.DeepEqual(suspended, want) {
		t.Fatalf("suspended = %v, want %v", suspended, want)
	}
}

func TestSuspendFireworksAccountModels_SkipsEmptyAccountID(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth := &Auth{
		ID:       "fw-1",
		Provider: "fireworks",
		Metadata: map[string]any{},
	}
	if _, err := m.Register(nil, auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
	})

	suspended := SuspendFireworksAccountModels(nil, m, reg, auth, "m1", "account_suspended")
	if len(suspended) != 0 {
		t.Fatalf("suspended = %v, want empty", suspended)
	}
}

func TestSuspendFireworksAccountModels_SkipsDifferentProvider(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{
		ID:       "fw-1",
		Provider: "fireworks",
		Metadata: map[string]any{"account_id": "acc-1"},
	}
	auth2 := &Auth{
		ID:       "claude-1",
		Provider: "claude",
		Metadata: map[string]any{"account_id": "acc-1"},
	}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	reg.RegisterClient("claude-1", "claude", []*registry.ModelInfo{{ID: "m1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("claude-1")
	})

	suspended := SuspendFireworksAccountModels(nil, m, reg, auth1, "m1", "account_suspended")
	if len(suspended) != 0 {
		t.Fatalf("suspended = %v, want empty", suspended)
	}
}

func TestSuspendFireworksAccountModels_FallsBackToAttributesAccountID(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{
		ID:         "fw-1",
		Provider:   "fireworks",
		Attributes: map[string]string{"account_id": "acc-1"},
	}
	auth2 := &Auth{
		ID:         "fw-2",
		Provider:   "fireworks",
		Attributes: map[string]string{"account_id": "acc-1"},
	}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	reg.RegisterClient("fw-2", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
	})

	suspended := SuspendFireworksAccountModels(nil, m, reg, auth1, "m1", "account_suspended")
	sort.Strings(suspended)

	want := []string{"fw-2"}
	if !reflect.DeepEqual(suspended, want) {
		t.Fatalf("suspended = %v, want %v", suspended, want)
	}
}

func TestSuspendFireworksAccountModels_NonStringAccountIDMetadata(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{ID: "fw-1", Provider: "fireworks", Metadata: map[string]any{"account_id": []byte("acc-1")}}
	auth2 := &Auth{ID: "fw-2", Provider: "fireworks", Metadata: map[string]any{"account_id": []byte("acc-1")}}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	reg.RegisterClient("fw-2", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
	})

	suspended := SuspendFireworksAccountModels(nil, m, reg, auth1, "m1", "account_suspended")
	sort.Strings(suspended)

	want := []string{"fw-2"}
	if !reflect.DeepEqual(suspended, want) {
		t.Fatalf("suspended = %v, want %v", suspended, want)
	}
}

func TestSuspendFireworksAccountModels_SetsSiblingStatusError(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{ID: "fw-1", Provider: "fireworks", Metadata: map[string]any{"account_id": "acc-1"}}
	auth2 := &Auth{ID: "fw-2", Provider: "fireworks", Metadata: map[string]any{"account_id": "acc-1"}}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	reg.RegisterClient("fw-2", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
	})

	SuspendFireworksAccountModels(nil, m, reg, auth1, "m1", "account_suspended")

	updated2, ok := m.GetByID("fw-2")
	if !ok || updated2 == nil {
		t.Fatalf("fw-2 not found after suspension")
	}
	if updated2.Status != StatusError {
		t.Fatalf("fw-2.Status = %v, want StatusError", updated2.Status)
	}
}

func TestSuspendFireworksAccountModels_IgnoresSiblingDisableCooling(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{ID: "fw-1", Provider: "fireworks", Metadata: map[string]any{"account_id": "acc-1"}}
	auth2 := &Auth{ID: "fw-2", Provider: "fireworks", Metadata: map[string]any{"account_id": "acc-1", "disable_cooling": true}}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	reg.RegisterClient("fw-2", "fireworks", []*registry.ModelInfo{{ID: "m1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
	})

	SuspendFireworksAccountModels(nil, m, reg, auth1, "m1", "account_suspended")

	updated2, ok := m.GetByID("fw-2")
	if !ok || updated2 == nil {
		t.Fatalf("fw-2 not found after suspension")
	}
	state := updated2.ModelStates["m1"]
	if state == nil {
		t.Fatalf("fw-2 model state for m1 is nil")
	}
	if state.NextRetryAfter.IsZero() {
		t.Fatalf("fw-2 model m1 NextRetryAfter is zero, want non-zero suspension even with disable_cooling")
	}
}

func TestSuspendFireworksAccountModels_SuspendsAllModelsWhenModelEmpty(t *testing.T) {
	m := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()

	auth1 := &Auth{
		ID:       "fw-1",
		Provider: "fireworks",
		Metadata: map[string]any{"account_id": "acc-1"},
	}
	auth2 := &Auth{
		ID:       "fw-2",
		Provider: "fireworks",
		Metadata: map[string]any{"account_id": "acc-1"},
	}

	if _, err := m.Register(nil, auth1); err != nil {
		t.Fatalf("register auth1: %v", err)
	}
	if _, err := m.Register(nil, auth2); err != nil {
		t.Fatalf("register auth2: %v", err)
	}

	reg.RegisterClient("fw-1", "fireworks", []*registry.ModelInfo{{ID: "m-empty-1"}, {ID: "m-empty-2"}})
	reg.RegisterClient("fw-2", "fireworks", []*registry.ModelInfo{{ID: "m-empty-1"}, {ID: "m-empty-2"}})
	t.Cleanup(func() {
		reg.UnregisterClient("fw-1")
		reg.UnregisterClient("fw-2")
	})

	suspended := SuspendFireworksAccountModels(nil, m, reg, auth1, "", "account_suspended")
	sort.Strings(suspended)

	want := []string{"fw-1", "fw-2"}
	if !reflect.DeepEqual(suspended, want) {
		t.Fatalf("suspended = %v, want %v", suspended, want)
	}

	available := reg.GetAvailableModels("openai")
	for _, modelMap := range available {
		if id, ok := modelMap["id"].(string); ok && (id == "m-empty-1" || id == "m-empty-2") {
			t.Fatalf("model %q should be suspended for the whole account, but found in available models", id)
		}
	}
}

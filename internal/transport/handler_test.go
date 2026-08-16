package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/service"
	"github.com/arctic-express/scheduler/internal/store"
)

// testEnv bundles a handler with its underlying services for test setup.
type testEnv struct {
	handler *Handler
	store   *store.Store
	resSvc  *service.ReservationService
	delSvc  *service.DeliveryService
	recSvc  *service.RecoveryService
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st := store.New()
	notify := &testNotifier{}
	resSvc := service.NewReservationService(st, notify, 10*time.Millisecond)
	delSvc := service.NewDeliveryService(st, notify)
	recSvc := service.NewRecoveryService(st, notify)
	h := NewHandler(st, resSvc, delSvc, recSvc)
	return &testEnv{handler: h, store: st, resSvc: resSvc, delSvc: delSvc, recSvc: recSvc}
}

type testNotifier struct {
	messages []string
}

func (n *testNotifier) Notify(ownerID, message string) {
	n.messages = append(n.messages, ownerID+": "+message)
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func setupFullEnv(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	router := NewRouter(env.handler)

	cutoff := time.Now().Add(48 * time.Hour)
	// Create window.
	doJSON(t, router, "POST", "/api/windows", map[string]any{
		"id":               "win-1",
		"voyage_id":        "voy-1",
		"origin_port":      "CNYTN",
		"destination_port": "NLRTM",
		"cutoff_time":      cutoff.Format(time.RFC3339Nano),
		"start_time":       cutoff.Add(-48 * time.Hour).Format(time.RFC3339Nano),
		"end_time":         cutoff.Add(24 * time.Hour).Format(time.RFC3339Nano),
		"capacity":         1,
		"oversell_ratio":   0.1,
	})
	// Create slot.
	doJSON(t, router, "POST", "/api/slots", map[string]any{
		"id":        "slot-1",
		"window_id": "win-1",
		"voyage_id": "voy-1",
		"capacity":  1,
	})
	// Register containers.
	for _, c := range []struct {
		id, owner, cargo, dest string
	}{
		{"c1", "o1", "normal", "NLRTM"},
		{"c2", "o2", "high_value", "NLRTM"},
	} {
		doJSON(t, router, "POST", "/api/containers", map[string]any{
			"id": c.id, "owner_id": c.owner, "cargo_type": c.cargo, "destination_port": c.dest,
		})
	}
	return env
}

func TestHandler_LockSlotAndWaitlist(t *testing.T) {
	env := setupFullEnv(t)
	router := NewRouter(env.handler)
	defer env.resSvc.Close()

	// Lock slot-1 with c1 (normal cargo) — should succeed.
	rec := doJSON(t, router, "POST", "/api/slots/slot-1/lock", map[string]any{
		"request_id":   "req-1",
		"container_id": "c1",
		"owner_id":     "o1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result service.LockResult
	json.NewDecoder(rec.Body).Decode(&result)
	if !result.Success {
		t.Fatalf("expected lock success: %+v", result)
	}

	// Lock slot-1 with c2 (high-value cargo) — should go to waitlist.
	rec2 := doJSON(t, router, "POST", "/api/slots/slot-1/lock", map[string]any{
		"request_id":   "req-2",
		"container_id": "c2",
		"owner_id":     "o2",
	})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var result2 service.LockResult
	json.NewDecoder(rec2.Body).Decode(&result2)
	if result2.Success {
		t.Fatal("expected lock to be queued")
	}
	if result2.QueuePosition != 1 {
		t.Fatalf("expected queue position 1, got %d", result2.QueuePosition)
	}

	// Check waitlist via API.
	rec3 := doJSON(t, router, "GET", "/api/windows/win-1/waitlist", nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec3.Code)
	}
	var wl map[string]any
	json.NewDecoder(rec3.Body).Decode(&wl)
	if wl["length"].(float64) != 1 {
		t.Fatalf("expected waitlist length 1, got %v", wl["length"])
	}

	// Get reservation via API.
	rec4 := doJSON(t, router, "GET", "/api/reservations/req-1", nil)
	if rec4.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec4.Code)
	}
}

func TestHandler_FreezeAndReschedule(t *testing.T) {
	env := newTestEnv(t)
	router := NewRouter(env.handler)
	defer env.resSvc.Close()

	cutoff := time.Now().Add(48 * time.Hour)
	// Two windows on same route.
	for _, w := range []struct {
		id, voy string
	}{
		{"win-1", "voy-1"},
		{"win-2", "voy-2"},
	} {
		doJSON(t, router, "POST", "/api/windows", map[string]any{
			"id": w.id, "voyage_id": w.voy, "origin_port": "CNYTN", "destination_port": "NLRTM",
			"cutoff_time":    cutoff.Format(time.RFC3339Nano),
			"start_time":     cutoff.Add(-48 * time.Hour).Format(time.RFC3339Nano),
			"end_time":       cutoff.Add(24 * time.Hour).Format(time.RFC3339Nano),
			"capacity":       2,
			"oversell_ratio": 0.1,
		})
		doJSON(t, router, "POST", "/api/slots", map[string]any{
			"id": "slot-" + w.voy, "window_id": w.id, "voyage_id": w.voy, "capacity": 2,
		})
	}
	doJSON(t, router, "POST", "/api/containers", map[string]any{
		"id": "c1", "owner_id": "o1", "cargo_type": "normal", "destination_port": "NLRTM",
	})

	// Lock a slot.
	rec := doJSON(t, router, "POST", "/api/slots/slot-voy-1/lock", map[string]any{
		"request_id": "req-1", "container_id": "c1", "owner_id": "o1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("lock failed: %d %s", rec.Code, rec.Body.String())
	}

	// Freeze the slot.
	rec2 := doJSON(t, router, "POST", "/api/slots/slot-voy-1/freeze", map[string]any{
		"reason": "schedule delay",
	})
	if rec2.Code != http.StatusOK {
		t.Fatalf("freeze failed: %d %s", rec2.Code, rec2.Body.String())
	}
	env.store.Mu.Lock()
	slot, _ := env.store.GetSlot("slot-voy-1")
	env.store.Mu.Unlock()
	if !slot.Frozen {
		t.Fatal("expected slot to be frozen")
	}

	// Reschedule to next voyage.
	rec3 := doJSON(t, router, "POST", "/api/reservations/req-1/reschedule", map[string]any{
		"next_slot_id": "slot-voy-2",
	})
	if rec3.Code != http.StatusOK {
		t.Fatalf("reschedule failed: %d %s", rec3.Code, rec3.Body.String())
	}
	env.store.Mu.Lock()
	r, _ := env.store.GetReservation("req-1")
	env.store.Mu.Unlock()
	if r.SlotID != "slot-voy-2" {
		t.Fatalf("expected slot-voy-2, got %s", r.SlotID)
	}
}

func TestHandler_HealthCheck(t *testing.T) {
	env := newTestEnv(t)
	router := NewRouter(env.handler)
	defer env.resSvc.Close()

	rec := doJSON(t, router, "GET", "/api/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHandler_OversellExceedsLimit(t *testing.T) {
	env := newTestEnv(t)
	router := NewRouter(env.handler)
	defer env.resSvc.Close()

	cutoff := time.Now().Add(48 * time.Hour)
	rec := doJSON(t, router, "POST", "/api/windows", map[string]any{
		"id": "win-bad", "voyage_id": "voy-1", "origin_port": "CNYTN", "destination_port": "NLRTM",
		"cutoff_time":    cutoff.Format(time.RFC3339Nano),
		"start_time":     cutoff.Add(-48 * time.Hour).Format(time.RFC3339Nano),
		"end_time":       cutoff.Add(24 * time.Hour).Format(time.RFC3339Nano),
		"capacity":       10,
		"oversell_ratio": 0.2,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversell ratio > 10%%, got %d", rec.Code)
	}
}

// Ensure context cancellation is handled gracefully.
func TestHandler_LockSlotContextCancel(t *testing.T) {
	env := newTestEnv(t)
	defer env.resSvc.Close()

	// Create a minimal setup so the service doesn't panic on nil window.
	env.store.Mu.Lock()
	env.store.SaveWindow(&domain.PeakWindow{ID: "w1", Capacity: 1, CutoffTime: time.Now().Add(48 * time.Hour)})
	env.store.SaveSlot(&domain.Slot{ID: "s1", WindowID: "w1", Capacity: 1, Status: domain.SlotStatusAvailable})
	env.store.Mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // ensure deadline has passed

	_, err := env.resSvc.LockSlot(ctx, service.LockSlotRequest{
		RequestID: "req-cancel", WindowID: "w1", ContainerID: "c1", OwnerID: "o1",
	}, domain.CargoTypeNormal)
	if err == nil {
		t.Fatal("expected context deadline exceeded error")
	}
}

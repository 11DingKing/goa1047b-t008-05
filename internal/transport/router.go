package transport

import "net/http"

// NewRouter returns an http.Handler with all API routes registered.
// Uses the Go 1.22+ pattern-based ServeMux.
func NewRouter(h *Handler) http.Handler {
	mux := http.NewServeMux()

	// Window management (dispatcher).
	mux.HandleFunc("POST /api/windows", h.CreateWindow)
	mux.HandleFunc("GET /api/windows", h.ListWindows)

	// Slot management.
	mux.HandleFunc("POST /api/slots", h.CreateSlot)
	mux.HandleFunc("GET /api/slots", h.ListSlots)
	mux.HandleFunc("POST /api/slots/{id}/lock", h.LockSlot)
	mux.HandleFunc("POST /api/slots/{id}/confirm-oversell", h.ConfirmOversell)
	mux.HandleFunc("POST /api/slots/{id}/manifest", h.VerifyManifest)
	mux.HandleFunc("POST /api/slots/{id}/freeze", h.FreezeSlot)
	mux.HandleFunc("POST /api/slots/{id}/unfreeze", h.UnfreezeSlot)

	// Container management (cargo owner, tally clerk).
	mux.HandleFunc("POST /api/containers", h.RegisterContainer)
	mux.HandleFunc("POST /api/containers/{id}/arrive", h.RecordArrival)
	mux.HandleFunc("POST /api/containers/{id}/temperature", h.RecordTemperature)

	// Reservation & delivery.
	mux.HandleFunc("GET /api/reservations/{id}", h.GetReservation)
	mux.HandleFunc("POST /api/reservations/{id}/deliver", h.ConfirmDelivery)
	mux.HandleFunc("POST /api/reservations/{id}/reschedule", h.RescheduleReservation)

	// Waitlist.
	mux.HandleFunc("GET /api/windows/{id}/waitlist", h.GetWaitlist)

	// Health check.
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	return mux
}

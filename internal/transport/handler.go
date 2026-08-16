package transport

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/service"
	"github.com/arctic-express/scheduler/internal/store"
)

// Handler exposes all HTTP endpoints for the slot-scheduling API.
type Handler struct {
	store       *store.Store
	reservation *service.ReservationService
	delivery    *service.DeliveryService
	recovery    *service.RecoveryService
}

// NewHandler creates a Handler wired to the given services.
func NewHandler(st *store.Store, res *service.ReservationService, del *service.DeliveryService, rec *service.RecoveryService) *Handler {
	return &Handler{store: st, reservation: res, delivery: del, recovery: rec}
}

// --- request / response helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// --- window endpoints ---

type createWindowReq struct {
	ID              string    `json:"id"`
	VoyageID        string    `json:"voyage_id"`
	OriginPort      string    `json:"origin_port"`
	DestinationPort string    `json:"destination_port"`
	CutoffTime      time.Time `json:"cutoff_time"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	Capacity        int       `json:"capacity"`
	OversellRatio   float64   `json:"oversell_ratio"`
}

func (h *Handler) CreateWindow(w http.ResponseWriter, r *http.Request) {
	var req createWindowReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.OversellRatio > 0.1 {
		writeError(w, http.StatusBadRequest, "oversell ratio must not exceed 10%")
		return
	}
	win := &domain.PeakWindow{
		ID:              req.ID,
		VoyageID:        req.VoyageID,
		OriginPort:      req.OriginPort,
		DestinationPort: req.DestinationPort,
		CutoffTime:      req.CutoffTime,
		StartTime:       req.StartTime,
		EndTime:         req.EndTime,
		Capacity:        req.Capacity,
		OversellRatio:   req.OversellRatio,
	}
	h.store.Mu.Lock()
	h.store.SaveWindow(win)
	h.store.Mu.Unlock()
	writeJSON(w, http.StatusCreated, win)
}

func (h *Handler) ListWindows(w http.ResponseWriter, r *http.Request) {
	h.store.Mu.Lock()
	ws := h.store.ListWindows()
	h.store.Mu.Unlock()
	writeJSON(w, http.StatusOK, ws)
}

// --- slot endpoints ---

type createSlotReq struct {
	ID       string `json:"id"`
	WindowID string `json:"window_id"`
	VoyageID string `json:"voyage_id"`
	Capacity int    `json:"capacity"`
}

func (h *Handler) CreateSlot(w http.ResponseWriter, r *http.Request) {
	var req createSlotReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.store.Mu.Lock()
	if _, ok := h.store.GetWindow(req.WindowID); !ok {
		h.store.Mu.Unlock()
		writeError(w, http.StatusBadRequest, "window not found")
		return
	}
	slot := &domain.Slot{
		ID:       req.ID,
		WindowID: req.WindowID,
		VoyageID: req.VoyageID,
		Capacity: req.Capacity,
		Status:   domain.SlotStatusAvailable,
	}
	h.store.SaveSlot(slot)
	h.store.Mu.Unlock()
	writeJSON(w, http.StatusCreated, slot)
}

func (h *Handler) ListSlots(w http.ResponseWriter, r *http.Request) {
	h.store.Mu.Lock()
	slots := h.store.ListSlots()
	h.store.Mu.Unlock()
	writeJSON(w, http.StatusOK, slots)
}

type lockSlotReq struct {
	RequestID   string `json:"request_id"`
	ContainerID string `json:"container_id"`
	OwnerID     string `json:"owner_id"`
}

func (h *Handler) LockSlot(w http.ResponseWriter, r *http.Request) {
	slotID := r.PathValue("id")
	var req lockSlotReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.store.Mu.Lock()
	slot, ok := h.store.GetSlot(slotID)
	if !ok {
		h.store.Mu.Unlock()
		writeError(w, http.StatusNotFound, "slot not found")
		return
	}
	c, ok := h.store.GetContainer(req.ContainerID)
	if !ok {
		h.store.Mu.Unlock()
		writeError(w, http.StatusBadRequest, "container not found")
		return
	}
	windowID := slot.WindowID
	h.store.Mu.Unlock()

	result, err := h.reservation.LockSlot(r.Context(), service.LockSlotRequest{
		RequestID:   req.RequestID,
		WindowID:    windowID,
		ContainerID: req.ContainerID,
		OwnerID:     req.OwnerID,
	}, c.CargoType)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	status := http.StatusOK
	if !result.Success {
		status = http.StatusConflict
	}
	writeJSON(w, status, result)
}

func (h *Handler) ConfirmOversell(w http.ResponseWriter, r *http.Request) {
	slotID := r.PathValue("id")
	if err := h.reservation.ConfirmOversell(slotID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "oversell confirmed"})
}

// --- container endpoints ---

type registerContainerReq struct {
	ID              string `json:"id"`
	OwnerID         string `json:"owner_id"`
	CargoType       string `json:"cargo_type"`
	DestinationPort string `json:"destination_port"`
}

func (h *Handler) RegisterContainer(w http.ResponseWriter, r *http.Request) {
	var req registerContainerReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c := &domain.Container{
		ID:              req.ID,
		OwnerID:         req.OwnerID,
		CargoType:       domain.CargoType(req.CargoType),
		DestinationPort: req.DestinationPort,
		Status:          domain.ContainerRegistered,
	}
	h.store.Mu.Lock()
	h.store.SaveContainer(c)
	h.store.Mu.Unlock()
	writeJSON(w, http.StatusCreated, c)
}

type arriveReq struct {
	ArrivedAt time.Time `json:"arrived_at"`
}

func (h *Handler) RecordArrival(w http.ResponseWriter, r *http.Request) {
	containerID := r.PathValue("id")
	var req arriveReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ts := req.ArrivedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if err := h.delivery.RecordCargoArrival(containerID, ts); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "arrived"})
}

type temperatureReq struct {
	Timestamp   time.Time `json:"timestamp"`
	Temperature float64   `json:"temperature"`
}

func (h *Handler) RecordTemperature(w http.ResponseWriter, r *http.Request) {
	containerID := r.PathValue("id")
	var req temperatureReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ts := req.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if err := h.delivery.RecordTemperature(containerID, ts, req.Temperature); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
}

// --- manifest & delivery endpoints ---

func (h *Handler) VerifyManifest(w http.ResponseWriter, r *http.Request) {
	slotID := r.PathValue("id")
	loaded, err := h.delivery.VerifyLoadingManifest(slotID, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"loaded": loaded})
}

func (h *Handler) ConfirmDelivery(w http.ResponseWriter, r *http.Request) {
	reservationID := r.PathValue("id")
	if err := h.delivery.ConfirmDelivery(reservationID, time.Now().UTC()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "delivered"})
}

// --- recovery endpoints ---

type freezeReq struct {
	Reason string `json:"reason"`
}

func (h *Handler) FreezeSlot(w http.ResponseWriter, r *http.Request) {
	slotID := r.PathValue("id")
	var req freezeReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	affected, err := h.recovery.FreezeSlot(slotID, req.Reason, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"frozen": true, "affected": affected})
}

func (h *Handler) UnfreezeSlot(w http.ResponseWriter, r *http.Request) {
	slotID := r.PathValue("id")
	if err := h.recovery.UnfreezeSlot(slotID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "unfrozen"})
}

type rescheduleReq struct {
	NextSlotID  string `json:"next_slot_id"`
	UseSuez     bool   `json:"use_suez"`
	SuezConsent bool   `json:"suez_consent"`
}

func (h *Handler) RescheduleReservation(w http.ResponseWriter, r *http.Request) {
	reservationID := r.PathValue("id")
	var req rescheduleReq
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	err := h.recovery.RescheduleReservation(reservationID, req.NextSlotID, req.UseSuez, req.SuezConsent, time.Now().UTC())
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrSuezConsentRequired):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, domain.ErrSlotFull):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, domain.ErrSameRouteOnly) || errors.Is(err, domain.ErrSameVoyage):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusNotFound, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rescheduled"})
}

// --- waitlist endpoint ---

func (h *Handler) GetWaitlist(w http.ResponseWriter, r *http.Request) {
	windowID := r.PathValue("id")
	wl := h.reservation.GetWaitlist(windowID)
	writeJSON(w, http.StatusOK, map[string]any{"window_id": windowID, "queue": wl, "length": len(wl)})
}

// --- reservation lookup ---

func (h *Handler) GetReservation(w http.ResponseWriter, r *http.Request) {
	reservationID := r.PathValue("id")
	h.store.Mu.Lock()
	r2, ok := h.store.GetReservation(reservationID)
	h.store.Mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "reservation not found")
		return
	}
	writeJSON(w, http.StatusOK, r2)
}

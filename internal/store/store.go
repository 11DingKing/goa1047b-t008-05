package store

import (
	"sync"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
)

// Store is a thread-safe in-memory persistence layer. The exported Mu mutex
// must be held by callers performing compound read-modify-write sequences;
// individual accessor methods are NOT locked to avoid reentrant-deadlock.
type Store struct {
	Mu           sync.Mutex
	windows      map[string]*domain.PeakWindow
	slots        map[string]*domain.Slot
	containers   map[string]*domain.Container
	reservations map[string]*domain.Reservation
	waitlists    map[string][]string
	events       []domain.Event
}

// New creates an empty Store.
func New() *Store {
	return &Store{
		windows:      make(map[string]*domain.PeakWindow),
		slots:        make(map[string]*domain.Slot),
		containers:   make(map[string]*domain.Container),
		reservations: make(map[string]*domain.Reservation),
		waitlists:    make(map[string][]string),
	}
}

// --- Windows ---

func (s *Store) SaveWindow(w *domain.PeakWindow) {
	s.windows[w.ID] = w
}

func (s *Store) GetWindow(id string) (*domain.PeakWindow, bool) {
	w, ok := s.windows[id]
	return w, ok
}

func (s *Store) ListWindows() []*domain.PeakWindow {
	out := make([]*domain.PeakWindow, 0, len(s.windows))
	for _, w := range s.windows {
		out = append(out, w)
	}
	return out
}

func (s *Store) DeleteWindow(id string) {
	delete(s.windows, id)
}

// --- Slots ---

func (s *Store) SaveSlot(slot *domain.Slot) {
	s.slots[slot.ID] = slot
}

func (s *Store) GetSlot(id string) (*domain.Slot, bool) {
	slot, ok := s.slots[id]
	return slot, ok
}

func (s *Store) GetSlotByWindow(windowID string) (*domain.Slot, bool) {
	for _, slot := range s.slots {
		if slot.WindowID == windowID {
			return slot, true
		}
	}
	return nil, false
}

func (s *Store) ListSlots() []*domain.Slot {
	out := make([]*domain.Slot, 0, len(s.slots))
	for _, slot := range s.slots {
		out = append(out, slot)
	}
	return out
}

// --- Containers ---

func (s *Store) SaveContainer(c *domain.Container) {
	s.containers[c.ID] = c
}

func (s *Store) GetContainer(id string) (*domain.Container, bool) {
	c, ok := s.containers[id]
	return c, ok
}

func (s *Store) ListContainers() []*domain.Container {
	out := make([]*domain.Container, 0, len(s.containers))
	for _, c := range s.containers {
		out = append(out, c)
	}
	return out
}

// --- Reservations ---

func (s *Store) SaveReservation(r *domain.Reservation) {
	s.reservations[r.ID] = r
}

func (s *Store) GetReservation(id string) (*domain.Reservation, bool) {
	r, ok := s.reservations[id]
	return r, ok
}

func (s *Store) ReservationsByOwner(ownerID string) []*domain.Reservation {
	out := make([]*domain.Reservation, 0)
	for _, r := range s.reservations {
		if r.OwnerID == ownerID {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) ReservationsBySlot(slotID string) []*domain.Reservation {
	out := make([]*domain.Reservation, 0)
	for _, r := range s.reservations {
		if r.SlotID == slotID {
			out = append(out, r)
		}
	}
	return out
}

func (s *Store) ReservationsByWindow(windowID string) []*domain.Reservation {
	out := make([]*domain.Reservation, 0)
	for _, r := range s.reservations {
		if r.WindowID == windowID {
			out = append(out, r)
		}
	}
	return out
}

// --- Waitlist ---

// AddToWaitlist appends a reservation ID to the window's waitlist and returns
// its 1-based position.
func (s *Store) AddToWaitlist(windowID, reservationID string) int {
	s.waitlists[windowID] = append(s.waitlists[windowID], reservationID)
	return len(s.waitlists[windowID])
}

func (s *Store) GetWaitlist(windowID string) []string {
	list := s.waitlists[windowID]
	out := make([]string, len(list))
	copy(out, list)
	return out
}

func (s *Store) RemoveFromWaitlist(windowID, reservationID string) {
	list := s.waitlists[windowID]
	for i, id := range list {
		if id == reservationID {
			s.waitlists[windowID] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

func (s *Store) WaitlistPosition(windowID, reservationID string) int {
	for i, id := range s.waitlists[windowID] {
		if id == reservationID {
			return i + 1
		}
	}
	return 0
}

// --- Events ---

func (s *Store) AppendEvent(e domain.Event) {
	s.events = append(s.events, e)
}

func (s *Store) Events() []domain.Event {
	out := make([]domain.Event, len(s.events))
	copy(out, s.events)
	return out
}

func (s *Store) ClearEvents() {
	s.events = nil
}

// --- Snapshot / Restore ---

// Snapshot is a point-in-time copy of all store data, used for persistence.
type Snapshot struct {
	Windows      []*domain.PeakWindow  `json:"windows"`
	Slots        []*domain.Slot        `json:"slots"`
	Containers   []*domain.Container   `json:"containers"`
	Reservations []*domain.Reservation `json:"reservations"`
	Waitlists    map[string][]string   `json:"waitlists"`
	SavedAt      time.Time             `json:"saved_at"`
}

// Snapshot returns a deep copy of the current store state.
func (s *Store) Snapshot() *Snapshot {
	snap := &Snapshot{
		Windows:      make([]*domain.PeakWindow, 0, len(s.windows)),
		Slots:        make([]*domain.Slot, 0, len(s.slots)),
		Containers:   make([]*domain.Container, 0, len(s.containers)),
		Reservations: make([]*domain.Reservation, 0, len(s.reservations)),
		Waitlists:    make(map[string][]string),
		SavedAt:      time.Now().UTC(),
	}
	for _, w := range s.windows {
		snap.Windows = append(snap.Windows, w)
	}
	for _, sl := range s.slots {
		snap.Slots = append(snap.Slots, sl)
	}
	for _, c := range s.containers {
		snap.Containers = append(snap.Containers, c)
	}
	for _, r := range s.reservations {
		snap.Reservations = append(snap.Reservations, r)
	}
	for k, v := range s.waitlists {
		cp := make([]string, len(v))
		copy(cp, v)
		snap.Waitlists[k] = cp
	}
	return snap
}

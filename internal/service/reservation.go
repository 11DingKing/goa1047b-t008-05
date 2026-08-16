package service

import (
	"container/heap"
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/arctic-express/scheduler/internal/domain"
	"github.com/arctic-express/scheduler/internal/store"
)

// Notifier sends messages to cargo owners about reservation state changes.
type Notifier interface {
	Notify(ownerID string, message string)
}

// LockResult is returned from a slot-lock attempt.
type LockResult struct {
	Success       bool   `json:"success"`
	ReservationID string `json:"reservation_id"`
	QueuePosition int    `json:"queue_position,omitempty"`
	Message       string `json:"message"`
}

// LockSlotRequest is the input for a slot-lock operation.
type LockSlotRequest struct {
	RequestID   string `json:"request_id"`
	WindowID    string `json:"window_id"`
	ContainerID string `json:"container_id"`
	OwnerID     string `json:"owner_id"`
}

// ReservationService handles slot inquiry, locking, oversell confirmation,
// and waitlist management with concurrent-request priority sorting.
type ReservationService struct {
	store      *store.Store
	notifier   Notifier
	batchDelay time.Duration

	mu          sync.Mutex
	windowChans map[string]chan *lockRequest
	idempotency sync.Map // requestID -> *idemEntry
	stop        chan struct{}
}

type lockRequest struct {
	req    LockSlotRequest
	cargo  domain.CargoType
	ts     time.Time
	result chan *LockResult
}

type idemEntry struct {
	done   chan struct{}
	result *LockResult
}

// NewReservationService creates a ReservationService. The batchDelay controls
// how long the per-window processor waits to collect concurrent requests before
// sorting them by priority.
func NewReservationService(s *store.Store, n Notifier, batchDelay time.Duration) *ReservationService {
	return &ReservationService{
		store:       s,
		notifier:    n,
		batchDelay:  batchDelay,
		windowChans: make(map[string]chan *lockRequest),
		stop:        make(chan struct{}),
	}
}

// Close shuts down all window-processor goroutines.
func (s *ReservationService) Close() {
	select {
	case <-s.stop:
		return
	default:
	}
	close(s.stop)
	s.mu.Lock()
	for _, ch := range s.windowChans {
		close(ch)
	}
	s.windowChans = make(map[string]chan *lockRequest)
	s.mu.Unlock()
}

func (s *ReservationService) getOrCreateChan(windowID string) chan *lockRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.windowChans[windowID]
	if !ok {
		ch = make(chan *lockRequest, 256)
		s.windowChans[windowID] = ch
		go s.processWindow(windowID, ch)
	}
	return ch
}

// processWindow is the per-window goroutine that receives lock requests,
// batches concurrent ones, sorts by (priority desc, timestamp asc), and
// processes them in that order.
func (s *ReservationService) processWindow(windowID string, ch chan *lockRequest) {
	pq := &priorityQueue{}
	heap.Init(pq)
	for {
		select {
		case req, ok := <-ch:
			if !ok {
				return
			}
			heap.Push(pq, req)

			// Collect additional requests that arrive within the batch window.
			timer := time.NewTimer(s.batchDelay)
			draining := true
			for draining {
				select {
				case r := <-ch:
					heap.Push(pq, r)
				case <-timer.C:
					draining = false
				case <-s.stop:
					timer.Stop()
					return
				}
			}
			timer.Stop()

			// Process all collected requests in priority order.
			for pq.Len() > 0 {
				lr := heap.Pop(pq).(*lockRequest)
				result := s.tryLock(lr, windowID)
				lr.result <- result
			}
		case <-s.stop:
			return
		}
	}
}

// tryLock attempts to lock a slot for the given request. It must be called
// from the per-window processor goroutine so only one tryLock runs at a time
// per window. The store mutex is held for the compound read-modify-write.
func (s *ReservationService) tryLock(lr *lockRequest, windowID string) *LockResult {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	window, ok := s.store.GetWindow(windowID)
	if !ok {
		return &LockResult{Success: false, Message: "window not found"}
	}

	slot, ok := s.store.GetSlotByWindow(windowID)
	if !ok {
		return &LockResult{Success: false, Message: "slot not found for window"}
	}

	if slot.Frozen {
		return &LockResult{Success: false, Message: "slot is frozen, try another window"}
	}

	now := time.Now().UTC()
	reservation := &domain.Reservation{
		ID:          lr.req.RequestID,
		SlotID:      slot.ID,
		WindowID:    windowID,
		ContainerID: lr.req.ContainerID,
		OwnerID:     lr.req.OwnerID,
		CargoType:   lr.cargo,
		Status:      domain.ReservationPending,
		Priority:    lr.cargo.Priority(),
		CreatedAt:   lr.ts,
	}

	// If oversell is needed but not yet confirmed, queue the request.
	if slot.NeedsOversell(*window) && !slot.OversellConfirmed {
		s.store.SaveReservation(reservation)
		pos := s.store.AddToWaitlist(windowID, reservation.ID)
		reservation.Enqueue(pos)
		s.store.SaveReservation(reservation)
		s.store.AppendEvent(domain.WaitlistUpdatedEvent{
			WindowID:      windowID,
			ReservationID: reservation.ID,
			Position:      pos,
			OccurredAt:    now,
		})
		return &LockResult{
			Success:       false,
			ReservationID: reservation.ID,
			QueuePosition: pos,
			Message:       "slot at capacity; oversell confirmation required; added to waitlist",
		}
	}

	if slot.CanBook(*window) {
		slot.Book()
		reservation.Lock(now)
		s.store.SaveSlot(slot)
		s.store.SaveReservation(reservation)
		s.store.AppendEvent(domain.SlotLockedEvent{
			ReservationID: reservation.ID,
			SlotID:        slot.ID,
			WindowID:      windowID,
			OwnerID:       reservation.OwnerID,
			CargoType:     reservation.CargoType,
			OccurredAt:    now,
		})
		return &LockResult{
			Success:       true,
			ReservationID: reservation.ID,
			Message:       "slot locked successfully",
		}
	}

	// No capacity — add to waitlist.
	s.store.SaveReservation(reservation)
	pos := s.store.AddToWaitlist(windowID, reservation.ID)
	reservation.Enqueue(pos)
	s.store.SaveReservation(reservation)
	s.store.AppendEvent(domain.WaitlistUpdatedEvent{
		WindowID:      windowID,
		ReservationID: reservation.ID,
		Position:      pos,
		OccurredAt:    now,
	})
	if s.notifier != nil {
		s.notifier.Notify(reservation.OwnerID,
			"slot full; added to waitlist at position "+strconv.Itoa(pos))
	}
	return &LockResult{
		Success:       false,
		ReservationID: reservation.ID,
		QueuePosition: pos,
		Message:       "slot at capacity; added to waitlist",
	}
}

// LockSlot submits a lock request to the per-window processor and waits for
// the result. It is idempotent: repeating a request with the same RequestID
// returns the cached result without side effects.
func (s *ReservationService) LockSlot(ctx context.Context, req LockSlotRequest, cargo domain.CargoType) (*LockResult, error) {
	entry := &idemEntry{done: make(chan struct{})}
	actual, loaded := s.idempotency.LoadOrStore(req.RequestID, entry)
	if loaded {
		entry = actual.(*idemEntry)
		select {
		case <-entry.done:
			return entry.result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	ch := s.getOrCreateChan(req.WindowID)
	lr := &lockRequest{
		req:    req,
		cargo:  cargo,
		ts:     time.Now().UTC(),
		result: make(chan *LockResult, 1),
	}

	select {
	case ch <- lr:
	default:
		entry.result = &LockResult{Success: false, Message: "request queue full"}
		close(entry.done)
		return entry.result, errors.New("request queue full")
	}

	select {
	case result := <-lr.result:
		entry.result = result
		close(entry.done)
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ConfirmOversell enables oversell capacity for a slot (dispatcher action).
func (s *ReservationService) ConfirmOversell(slotID string) error {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	slot, ok := s.store.GetSlot(slotID)
	if !ok {
		return domain.ErrSlotNotFound
	}
	slot.ConfirmOversell()
	s.store.SaveSlot(slot)
	return nil
}

// GetWaitlistPosition returns the 1-based waitlist position for a reservation.
func (s *ReservationService) GetWaitlistPosition(windowID, reservationID string) (int, error) {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()

	pos := s.store.WaitlistPosition(windowID, reservationID)
	if pos == 0 {
		return 0, domain.ErrReservationNotFound
	}
	return pos, nil
}

// GetWaitlist returns the ordered list of reservation IDs in the waitlist.
func (s *ReservationService) GetWaitlist(windowID string) []string {
	s.store.Mu.Lock()
	defer s.store.Mu.Unlock()
	return s.store.GetWaitlist(windowID)
}

// --- priority queue for concurrent lock requests ---

type priorityQueue []*lockRequest

func (pq priorityQueue) Len() int { return len(pq) }

// Less sorts by priority descending, then by timestamp ascending (first-come-first-served
// within the same priority tier).
func (pq priorityQueue) Less(i, j int) bool {
	if pq[i].cargo.Priority() != pq[j].cargo.Priority() {
		return pq[i].cargo.Priority() > pq[j].cargo.Priority()
	}
	return pq[i].ts.Before(pq[j].ts)
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
}

func (pq *priorityQueue) Push(x any) {
	*pq = append(*pq, x.(*lockRequest))
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	x := old[n-1]
	*pq = old[:n-1]
	return x
}

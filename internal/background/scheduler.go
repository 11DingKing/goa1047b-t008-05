package background

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/arctic-express/scheduler/internal/service"
)

// Scheduler runs periodic background tasks: the 24-hour auto-release check
// and temperature-compliance monitoring for in-transit containers.
type Scheduler struct {
	delivery *service.DeliveryService
	interval time.Duration
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New creates a Scheduler that calls CheckAutoRelease and
// CheckTemperatureCompliance on the given interval.
func New(ds *service.DeliveryService, interval time.Duration) *Scheduler {
	return &Scheduler{
		delivery: ds,
		interval: interval,
		stop:     make(chan struct{}),
	}
}

// Start launches the scheduler goroutine.
func (s *Scheduler) Start() {
	s.wg.Add(1)
	go s.run()
}

// Stop signals the scheduler to stop and waits for the goroutine to exit.
func (s *Scheduler) Stop() {
	select {
	case <-s.stop:
		return
	default:
	}
	close(s.stop)
	s.wg.Wait()
}

func (s *Scheduler) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.tick(time.Now().UTC())
		}
	}
}

// tick executes one cycle of background checks.
func (s *Scheduler) tick(now time.Time) {
	released := s.delivery.CheckAutoRelease(now)
	if len(released) > 0 {
		log.Printf("auto-released %d reservations", len(released))
	}
	alerts := s.delivery.CheckTemperatureCompliance(now)
	if len(alerts) > 0 {
		log.Printf("temperature alerts: %d containers", len(alerts))
	}
}

// RunOnce executes one cycle synchronously. Useful for tests and on-demand sweeps.
func (s *Scheduler) RunOnce(ctx context.Context) {
	s.tick(time.Now().UTC())
}

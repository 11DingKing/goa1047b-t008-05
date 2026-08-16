package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/arctic-express/scheduler/internal/background"
	"github.com/arctic-express/scheduler/internal/service"
	"github.com/arctic-express/scheduler/internal/store"
	"github.com/arctic-express/scheduler/internal/transport"
)

const defaultPort = "59235"

type loggingNotifier struct{}

func (n *loggingNotifier) Notify(ownerID, message string) {
	log.Printf("[NOTIFY] owner=%s: %s", ownerID, message)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	st := store.New()
	notifier := &loggingNotifier{}

	resSvc := service.NewReservationService(st, notifier, 20*time.Millisecond)
	delSvc := service.NewDeliveryService(st, notifier)
	recSvc := service.NewRecoveryService(st, notifier)

	sched := background.New(delSvc, 30*time.Second)
	sched.Start()
	defer sched.Stop()

	handler := transport.NewHandler(st, resSvc, delSvc, recSvc)
	router := transport.NewRouter(handler)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("arctic-express slot-scheduler listening on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resSvc.Close()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("server stopped")
}

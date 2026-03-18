package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/1919chichi/rc_1919chichi/internal/handler"
	"github.com/1919chichi/rc_1919chichi/internal/store"
	"github.com/1919chichi/rc_1919chichi/internal/worker"
)

func main() {
	dbPath := envOr("DB_PATH", "notifications.db")
	addr := envOr("ADDR", ":8080")

	db, err := store.New(dbPath)
	if err != nil {
		log.Fatalf("failed to init store: %v", err)
	}
	defer db.Close()
	log.Printf("database initialized at %s", dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcher := worker.New(db)
	go dispatcher.Start(ctx)

	mux := http.NewServeMux()
	h := handler.New(db)
	h.RegisterRoutes(mux)

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}

	log.Println("bye")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

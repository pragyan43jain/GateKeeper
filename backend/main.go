package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func newBackendHandler(backendID string) http.Handler {
	if backendID == "" {
		backendID = "backend"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/students", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Id", backendID)
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"backend_id": backendID,
			"message":    "Hello from backend",
			"students":   []string{"Arjun", "Priya", "Rahul"},
		})
	})

	return mux
}

func main() {
	backendID := os.Getenv("BACKEND_ID")
	handler := newBackendHandler(backendID)

	effectiveID := backendID
	if effectiveID == "" {
		effectiveID = "backend"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	log.Printf("Backend (%s) running at http://localhost:%s", effectiveID, port)
	server := &http.Server{Addr: ":" + port, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		drain, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(drain); err != nil {
			_ = server.Close()
		}
		close(done)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-done
}

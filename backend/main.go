package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

func newBackendHandler(backendID string) http.Handler {
	if backendID == "" {
		backendID = "backend"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/students", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Id", backendID)
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
	log.Printf("Backend (%s) running at http://localhost:8081", effectiveID)
	log.Fatal(http.ListenAndServe(":8081", handler))
}

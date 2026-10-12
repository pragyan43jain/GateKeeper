package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/students", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"message":  "Hello from backend",
			"students": []string{"Arjun", "Priya", "Rahul"},
		})
	})

	log.Println("Backend running at http://localhost:8081")
	log.Fatal(http.ListenAndServe(":8081", mux))
}

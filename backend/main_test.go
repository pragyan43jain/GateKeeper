package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestBackendHandler_BackendID(t *testing.T) {
	t.Run("CustomBackendID", func(t *testing.T) {
		handler := newBackendHandler("backend-1")
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rr.Code)
		}

		if got := rr.Header().Get("X-Backend-Id"); got != "backend-1" {
			t.Errorf("expected X-Backend-Id 'backend-1', got %q", got)
		}

		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to decode response JSON: %v", err)
		}

		if body["backend_id"] != "backend-1" {
			t.Errorf("expected backend_id 'backend-1', got %v", body["backend_id"])
		}
		if body["message"] != "Hello from backend" {
			t.Errorf("expected message 'Hello from backend', got %v", body["message"])
		}

		expectedStudents := []any{"Arjun", "Priya", "Rahul"}
		if !reflect.DeepEqual(body["students"], expectedStudents) {
			t.Errorf("expected students %v, got %v", expectedStudents, body["students"])
		}
	})

	t.Run("DefaultBackendIDWhenEmpty", func(t *testing.T) {
		handler := newBackendHandler("")
		req := httptest.NewRequest(http.MethodGet, "/api/students", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200 OK, got %d", rr.Code)
		}

		var body map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("failed to decode response JSON: %v", err)
		}

		if body["backend_id"] != "backend" {
			t.Errorf("expected default backend_id 'backend', got %v", body["backend_id"])
		}
	})

	t.Run("MethodNotAllowedForPOST", func(t *testing.T) {
		handler := newBackendHandler("backend-1")
		req := httptest.NewRequest(http.MethodPost, "/api/students", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 Method Not Allowed, got %d", rr.Code)
		}
	})
}

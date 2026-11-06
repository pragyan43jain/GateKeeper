package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func registerManagementRoutes(mux *http.ServeMux, store *ManagementStore, pool *BackendPool, routes *RouteRegistry, auth *ManagementAuth, limiter RateLimiter) {
	if store == nil {
		return
	}
	protect := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			principal, err := auth.Authenticate(r)
			if err != nil || !auth.CanRead(principal) {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/users") && principal.Role != "admin" {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			readOnly := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
			if !readOnly && !auth.CanManage(principal, r.URL.Path) {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			if limiter != nil {
				result, err := allowManagementRequest(r.Context(), limiter, "management:"+hashAPIKey(principal.ID))
				if err != nil {
					http.Error(w, "Service Unavailable: management rate limiter unavailable", http.StatusServiceUnavailable)
					return
				}
				if !result.Allowed {
					w.Header().Set("Retry-After", strconv.FormatInt(result.RetryAfter, 10))
					http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
					return
				}
				w.Header().Set("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
				w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
			}
			if readOnly {
				next(w, r)
				return
			}
			tracked := &auditResponseWriter{ResponseWriter: w}
			next(tracked, r)
			resourceType, resourceID := auditResource(r.URL.Path)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := store.RecordAuditEvent(ctx, principal.Email, r.Method, resourceType, resourceID, map[string]any{"status": tracked.status}); err != nil {
				log.Printf("audit_write_failed action=%s resource=%s", r.Method, resourceType)
			}
		}
	}
	backendMutationMu := &sync.Mutex{}
	if pool != nil {
		backendMutationMu = &pool.managementMu
	}
	mux.HandleFunc("/api/users", protect(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			users, err := store.ListDashboardUsers(r.Context())
			if err != nil {
				http.Error(w, "Failed to load dashboard users", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"users": users})
		case http.MethodPost:
			var input struct {
				Email    string `json:"email"`
				Password string `json:"password"`
				Role     string `json:"role"`
			}
			if err := decodeManagementJSON(w, r, &input); err != nil {
				http.Error(w, "Invalid user request", http.StatusBadRequest)
				return
			}
			email, err := normalizeDashboardCredentials(input.Email, input.Password)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if input.Role != "admin" && input.Role != "developer" && input.Role != "viewer" {
				http.Error(w, "Role must be admin, developer, or viewer", http.StatusBadRequest)
				return
			}
			hash, err := hashDashboardPassword(input.Password)
			if err != nil {
				http.Error(w, "Unable to prepare user credentials", http.StatusInternalServerError)
				return
			}
			user, err := store.CreateDashboardUser(r.Context(), email, hash, input.Role)
			if err != nil {
				http.Error(w, "Dashboard user could not be created", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusCreated, user)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/users/", protect(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/users/")
		if !uuidPattern.MatchString(id) {
			http.Error(w, "Invalid dashboard user ID", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPatch {
			var input struct {
				Role string `json:"role"`
			}
			if err := decodeManagementJSON(w, r, &input); err != nil {
				http.Error(w, "Invalid role update", http.StatusBadRequest)
				return
			}
			if input.Role != "admin" && input.Role != "developer" && input.Role != "viewer" {
				http.Error(w, "Role must be admin, developer, or viewer", http.StatusBadRequest)
				return
			}
			user, err := store.UpdateDashboardUserRole(r.Context(), id, input.Role)
			if err != nil {
				http.Error(w, "Dashboard user could not be updated", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, user)
			return
		}
		if r.Method == http.MethodDelete {
			principal, err := auth.Authenticate(r)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			if id == principal.ID {
				http.Error(w, "You cannot remove your own account", http.StatusConflict)
				return
			}
			if err := store.DeleteDashboardUser(r.Context(), id); err != nil {
				http.Error(w, "Dashboard user could not be removed", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Allow", "PATCH, DELETE")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}))
	mux.HandleFunc("/api/routes", protect(func(w http.ResponseWriter, r *http.Request) {
		if routes == nil {
			http.Error(w, "Route registry is unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			routeRecords, err := store.ListRoutes(r.Context())
			if err != nil {
				http.Error(w, "Failed to load routes", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"routes": routeRecords})
		case http.MethodPost:
			route, status, err := decodeRouteInput(w, r, true, ManagedRoute{})
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			backendMutationMu.Lock()
			defer backendMutationMu.Unlock()
			if err := validateRouteBackend(route, pool); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			created, err := store.CreateRoute(r.Context(), route)
			if err != nil {
				http.Error(w, "Route path conflicts with an existing route or backend", http.StatusConflict)
				return
			}
			routeRecords, err := store.ListRoutes(r.Context())
			if err == nil {
				err = routes.Replace(routeRecords)
			}
			if err != nil {
				_ = store.DeleteRoute(r.Context(), created.ID)
				if remaining, loadErr := store.ListRoutes(r.Context()); loadErr == nil {
					_ = routes.Replace(remaining)
				}
				http.Error(w, "Route could not be activated", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/routes/", protect(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/routes/")
		if !uuidPattern.MatchString(id) {
			http.Error(w, "Invalid route ID", http.StatusBadRequest)
			return
		}
		backendMutationMu.Lock()
		defer backendMutationMu.Unlock()
		records, err := store.ListRoutes(r.Context())
		if err != nil {
			http.Error(w, "Failed to load routes", http.StatusInternalServerError)
			return
		}
		var current ManagedRoute
		found := false
		for _, record := range records {
			if record.ID == id {
				current, found = record, true
				break
			}
		}
		if !found {
			http.Error(w, "Route not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPut:
			updated, status, err := decodeRouteInput(w, r, false, current)
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			if updated.Enabled {
				if err := validateRouteBackend(updated, pool); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
			updated, err = store.UpdateRoute(r.Context(), id, updated)
			if err != nil {
				http.Error(w, "Route update conflicts with existing configuration", http.StatusConflict)
				return
			}
			records, err = store.ListRoutes(r.Context())
			if err == nil {
				err = routes.Replace(records)
			}
			if err != nil {
				_, _ = store.UpdateRoute(r.Context(), id, current)
				if rollback, loadErr := store.ListRoutes(r.Context()); loadErr == nil {
					_ = routes.Replace(rollback)
				}
				http.Error(w, "Route configuration could not be activated", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, updated)
		case http.MethodDelete:
			if err := store.DeleteRoute(r.Context(), id); err != nil {
				http.Error(w, "Route could not be removed", http.StatusConflict)
				return
			}
			records, err = store.ListRoutes(r.Context())
			if err == nil {
				err = routes.Replace(records)
			}
			if err != nil {
				http.Error(w, "Route removal could not be activated", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "PUT, DELETE")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/backends", protect(func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "Backend pool is unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			records, err := store.ListBackends(r.Context())
			if err != nil {
				http.Error(w, "Failed to load backends", http.StatusInternalServerError)
				return
			}
			nodesByID := make(map[string]*BackendNode)
			for _, node := range pool.Nodes() {
				nodesByID[node.configID] = node
			}
			items := make([]map[string]any, 0, len(records))
			for _, record := range records {
				item := map[string]any{
					"configId": record.ID, "id": record.Name, "name": record.Name, "url": record.URL, "weight": record.Weight,
					"enabled": record.Enabled, "maintenance": record.Maintenance, "healthPath": record.HealthPath,
				}
				if node := nodesByID[record.ID]; node != nil {
					item["id"] = node.Name()
					item["weight"] = pool.Weight(node)
					state, failures, successes, checked, reason := node.HealthInfo()
					lastCheck := ""
					if !checked.IsZero() {
						lastCheck = checked.Format(time.RFC3339)
					}
					breaker := "closed"
					if node.cb != nil {
						breaker = strings.ToLower(node.cb.State().String())
					}
					item["healthStatus"] = state
					item["circuitBreakerState"] = breaker
					item["consecutiveFailures"] = failures
					item["consecutiveSuccesses"] = successes
					item["lastCheckTime"] = lastCheck
					item["lastError"] = reason
					item["activeRequests"] = node.ActiveRequests()
					item["strategy"] = string(pool.Strategy())
				} else if !record.Enabled {
					item["healthStatus"] = "disabled"
				} else {
					item["healthStatus"] = "maintenance"
				}
				items = append(items, item)
			}
			writeJSON(w, http.StatusOK, map[string]any{"backends": items})
		case http.MethodPost:
			backend, status, err := decodeBackendInput(w, r, true, ManagedBackend{})
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			backendMutationMu.Lock()
			defer backendMutationMu.Unlock()
			created, err := store.CreateBackend(r.Context(), backend)
			if err != nil {
				http.Error(w, "Backend name already exists or could not be stored", http.StatusConflict)
				return
			}
			records, err := store.ListBackends(r.Context())
			if err == nil {
				err = applyManagedBackends(pool, records, store, r.Context(), true)
			}
			if err != nil {
				_ = store.DeleteBackend(r.Context(), created.ID)
				if remaining, loadErr := store.ListBackends(r.Context()); loadErr == nil {
					_ = applyManagedBackends(pool, remaining, store, r.Context(), true)
				}
				http.Error(w, "Backend could not be activated", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusCreated, created)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/backends/", protect(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/backends/")
		if !uuidPattern.MatchString(id) {
			http.Error(w, "Invalid backend ID", http.StatusBadRequest)
			return
		}
		backendMutationMu.Lock()
		defer backendMutationMu.Unlock()
		records, err := store.ListBackends(r.Context())
		if err != nil {
			http.Error(w, "Failed to load backends", http.StatusInternalServerError)
			return
		}
		var current ManagedBackend
		found := false
		for _, record := range records {
			if record.ID == id {
				current, found = record, true
				break
			}
		}
		if !found {
			http.Error(w, "Backend not found", http.StatusNotFound)
			return
		}
		switch r.Method {
		case http.MethodPut:
			updated, status, err := decodeBackendInput(w, r, false, current)
			if err != nil {
				http.Error(w, err.Error(), status)
				return
			}
			if !updated.Enabled && enabledCount(records, id) == 0 {
				http.Error(w, "At least one backend must remain enabled", http.StatusConflict)
				return
			}
			if !updated.Enabled {
				routes, routeErr := store.ListRoutes(r.Context())
				if routeErr != nil {
					http.Error(w, "Failed to verify backend route references", http.StatusInternalServerError)
					return
				}
				for _, route := range routes {
					if route.Enabled && route.BackendID == id {
						http.Error(w, "Disable routes that use this backend before disabling the backend", http.StatusConflict)
						return
					}
				}
			}
			updated, err = store.UpdateBackend(r.Context(), id, updated)
			if err != nil {
				http.Error(w, "Backend update conflicts with existing configuration", http.StatusConflict)
				return
			}
			records, err = store.ListBackends(r.Context())
			if err == nil {
				err = applyManagedBackends(pool, records, store, r.Context(), true)
			}
			if err != nil {
				if _, rollbackErr := store.UpdateBackend(r.Context(), id, current); rollbackErr != nil {
					log.Printf("backend_configuration_rollback_failed backend_id=%s", id)
				}
				if rollback, loadErr := store.ListBackends(r.Context()); loadErr == nil {
					_ = applyManagedBackends(pool, rollback, store, r.Context(), true)
				}
				http.Error(w, "Backend configuration could not be activated", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusOK, updated)
		case http.MethodDelete:
			if enabledCount(records, id) == 0 {
				http.Error(w, "At least one backend must remain enabled", http.StatusConflict)
				return
			}
			if err := store.DeleteBackend(r.Context(), id); err != nil {
				http.Error(w, "Backend is referenced by a route or could not be removed", http.StatusConflict)
				return
			}
			records, err = store.ListBackends(r.Context())
			if err == nil {
				err = applyManagedBackends(pool, records, store, r.Context(), true)
			}
			if err != nil {
				if rollbackErr := store.RestoreBackend(r.Context(), current); rollbackErr != nil {
					log.Printf("backend_removal_rollback_failed backend_id=%s", id)
				}
				if rollback, loadErr := store.ListBackends(r.Context()); loadErr == nil {
					_ = applyManagedBackends(pool, rollback, store, r.Context(), true)
				}
				http.Error(w, "Backend removal could not be activated", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "PUT, DELETE")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/backend-health-history", protect(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		backendID := r.URL.Query().Get("backendId")
		if !uuidPattern.MatchString(backendID) {
			http.Error(w, "A valid backendId is required", http.StatusBadRequest)
			return
		}
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 100 {
				http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		events, err := store.ListBackendHealthHistory(r.Context(), backendID, limit)
		if err != nil {
			http.Error(w, "Failed to load backend health history", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": events, "count": len(events)})
	}))
	mux.HandleFunc("/api/audit-logs", protect(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 200 {
				http.Error(w, "limit must be between 1 and 200", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
		entries, err := store.ListAuditEvents(r.Context(), limit)
		if err != nil {
			http.Error(w, "Failed to load audit logs", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": entries, "count": len(entries)})
	}))
	mux.HandleFunc("/api/applications", protect(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			apps, err := store.ListApplications(r.Context())
			if err != nil {
				http.Error(w, "Failed to load applications", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"applications": apps})
		case http.MethodPost:
			var input struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if err := decodeManagementJSON(w, r, &input); err != nil {
				http.Error(w, "Invalid JSON request", http.StatusBadRequest)
				return
			}
			input.Name = strings.TrimSpace(input.Name)
			input.Description = strings.TrimSpace(input.Description)
			if input.Name == "" || len(input.Name) > 100 || len(input.Description) > 500 {
				http.Error(w, "Name is required (max 100 characters); description is limited to 500 characters", http.StatusBadRequest)
				return
			}
			app, err := store.CreateApplication(r.Context(), input.Name, input.Description)
			if err != nil {
				http.Error(w, "Failed to create application", http.StatusConflict)
				return
			}
			writeJSON(w, http.StatusCreated, app)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/settings/load-balancing", protect(func(w http.ResponseWriter, r *http.Request) {
		backendMutationMu.Lock()
		defer backendMutationMu.Unlock()
		if pool == nil {
			http.Error(w, "Backend pool is unavailable", http.StatusServiceUnavailable)
			return
		}
		switch r.Method {
		case http.MethodGet:
			settings := LoadBalancingSettings{Strategy: pool.Strategy(), Weights: make(map[string]int64)}
			for _, node := range pool.Nodes() {
				settings.Weights[node.Name()] = pool.Weight(node)
			}
			writeJSON(w, http.StatusOK, settings)
		case http.MethodPut:
			var settings LoadBalancingSettings
			if err := decodeManagementJSON(w, r, &settings); err != nil {
				http.Error(w, "Invalid JSON request", http.StatusBadRequest)
				return
			}
			if err := pool.ValidateConfiguration(settings.Strategy, settings.Weights); err != nil {
				http.Error(w, "Invalid load balancing configuration: "+err.Error(), http.StatusBadRequest)
				return
			}
			if err := store.SaveLoadBalancingSettings(r.Context(), settings); err != nil {
				http.Error(w, "Failed to persist load balancing configuration", http.StatusInternalServerError)
				return
			}
			if err := pool.Configure(settings.Strategy, settings.Weights); err != nil {
				http.Error(w, "Failed to apply load balancing configuration", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, settings)
		default:
			w.Header().Set("Allow", "GET, PUT")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/keys", protect(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			keys, err := store.ListAPIKeys(r.Context())
			if err != nil {
				http.Error(w, "Failed to load API keys", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
		case http.MethodPost:
			var input struct {
				ApplicationID     string   `json:"applicationId"`
				Name              string   `json:"name"`
				Permissions       []string `json:"permissions"`
				RequestsPerWindow int      `json:"requestsPerWindow"`
				WindowSeconds     int      `json:"windowSeconds"`
				Burst             int      `json:"burst"`
				ExpiresAt         string   `json:"expiresAt"`
			}
			if err := decodeManagementJSON(w, r, &input); err != nil {
				http.Error(w, "Invalid JSON request", http.StatusBadRequest)
				return
			}
			input.Name = strings.TrimSpace(input.Name)
			if !uuidPattern.MatchString(input.ApplicationID) || input.Name == "" || len(input.Name) > 100 {
				http.Error(w, "A valid applicationId and key name (max 100 characters) are required", http.StatusBadRequest)
				return
			}
			if input.RequestsPerWindow < 1 || input.RequestsPerWindow > 1_000_000 || input.WindowSeconds < 1 || input.WindowSeconds > 86_400 || input.Burst < 0 || input.Burst > 1_000_000 {
				http.Error(w, "Rate limit must be 1..1000000 requests per 1..86400 seconds and burst 0..1000000", http.StatusBadRequest)
				return
			}
			permissions, err := normalizePermissions(input.Permissions)
			if err != nil {
				http.Error(w, "Permissions may contain only read and write", http.StatusBadRequest)
				return
			}
			var expiresAt *time.Time
			if input.ExpiresAt != "" {
				parsed, err := time.Parse(time.RFC3339, input.ExpiresAt)
				if err != nil || !parsed.After(time.Now()) {
					http.Error(w, "expiresAt must be a future RFC3339 timestamp", http.StatusBadRequest)
					return
				}
				expiresAt = &parsed
			}
			key, err := store.CreateAPIKey(r.Context(), input.ApplicationID, input.Name, permissions, input.RequestsPerWindow, input.WindowSeconds, input.Burst, expiresAt)
			if err != nil {
				http.Error(w, "Failed to create API key; verify the application exists", http.StatusBadRequest)
				return
			}
			writeJSON(w, http.StatusCreated, key)
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		}
	}))
	mux.HandleFunc("/api/keys/", protect(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
		if r.Method == http.MethodPatch {
			var input struct {
				Permissions       []string `json:"permissions"`
				RequestsPerWindow int      `json:"requestsPerWindow"`
				WindowSeconds     int      `json:"windowSeconds"`
				Burst             int      `json:"burst"`
				ExpiresAt         *string  `json:"expiresAt"`
			}
			if err := decodeManagementJSON(w, r, &input); err != nil {
				http.Error(w, "Invalid JSON request", http.StatusBadRequest)
				return
			}
			if input.RequestsPerWindow < 1 || input.RequestsPerWindow > 1_000_000 || input.WindowSeconds < 1 || input.WindowSeconds > 86_400 || input.Burst < 0 || input.Burst > 1_000_000 {
				http.Error(w, "Rate limit must be 1..1000000 requests per 1..86400 seconds and burst 0..1000000", http.StatusBadRequest)
				return
			}
			permissions, err := normalizePermissions(input.Permissions)
			if err != nil {
				http.Error(w, "Permissions may contain only read and write", http.StatusBadRequest)
				return
			}
			var expiresAt *time.Time
			if input.ExpiresAt != nil && *input.ExpiresAt != "" {
				parsed, err := time.Parse(time.RFC3339, *input.ExpiresAt)
				if err != nil || !parsed.After(time.Now()) {
					http.Error(w, "expiresAt must be a future RFC3339 timestamp", http.StatusBadRequest)
					return
				}
				expiresAt = &parsed
			}
			if err := store.UpdateAPIKeyPolicy(r.Context(), id, permissions, input.RequestsPerWindow, input.WindowSeconds, input.Burst, expiresAt, input.ExpiresAt != nil); err != nil {
				if errors.Is(err, ErrAPIKeyNotFound) {
					http.Error(w, "API key not found or inactive", http.StatusNotFound)
					return
				}
				http.Error(w, "Failed to update API key policy", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", "PATCH, DELETE")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if !uuidPattern.MatchString(id) {
			http.Error(w, "Invalid API key ID", http.StatusBadRequest)
			return
		}
		if err := store.RevokeAPIKey(r.Context(), id); err != nil {
			if errors.Is(err, ErrAPIKeyNotFound) {
				http.Error(w, "API key not found or already revoked", http.StatusNotFound)
				return
			}
			http.Error(w, "Failed to revoke API key", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}

func enabledCount(backends []ManagedBackend, excludingID string) int {
	count := 0
	for _, backend := range backends {
		if backend.ID != excludingID && backend.Enabled {
			count++
		}
	}
	return count
}

func decodeBackendInput(w http.ResponseWriter, r *http.Request, creating bool, current ManagedBackend) (ManagedBackend, int, error) {
	var input struct {
		Name        string `json:"name"`
		URL         string `json:"url"`
		Weight      *int64 `json:"weight"`
		Enabled     *bool  `json:"enabled"`
		Maintenance *bool  `json:"maintenance"`
		HealthPath  string `json:"healthPath"`
	}
	if err := decodeManagementJSON(w, r, &input); err != nil {
		return ManagedBackend{}, http.StatusBadRequest, errors.New("invalid JSON request")
	}
	backend := current
	if input.Name != "" || creating {
		backend.Name = strings.TrimSpace(input.Name)
	}
	if input.URL != "" || creating {
		backend.URL = strings.TrimSpace(input.URL)
	}
	if input.Weight != nil {
		backend.Weight = *input.Weight
	} else if creating {
		backend.Weight = 1
	}
	if input.Enabled != nil {
		backend.Enabled = *input.Enabled
	} else if creating {
		backend.Enabled = true
	}
	if input.Maintenance != nil {
		backend.Maintenance = *input.Maintenance
	}
	if input.HealthPath != "" || creating {
		backend.HealthPath = input.HealthPath
		if backend.HealthPath == "" {
			backend.HealthPath = "/health"
		}
	}
	if backend.Name == "" || len(backend.Name) > 100 || backend.Weight < 1 || backend.Weight > 1000 {
		return ManagedBackend{}, http.StatusBadRequest, errors.New("name is required (max 100 characters) and weight must be between 1 and 1000")
	}
	u, err := url.Parse(backend.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Opaque != "" {
		return ManagedBackend{}, http.StatusBadRequest, errors.New("url must be an HTTP(S) URL with a host and without credentials or fragments")
	}
	if backend.HealthPath == "" || len(backend.HealthPath) > 200 || !strings.HasPrefix(backend.HealthPath, "/") || strings.Contains(backend.HealthPath, "..") || strings.ContainsAny(backend.HealthPath, "?#") {
		return ManagedBackend{}, http.StatusBadRequest, errors.New("healthPath must be a safe absolute path")
	}
	return backend, 0, nil
}

func applyManagedBackends(pool *BackendPool, records []ManagedBackend, store *ManagementStore, ctx context.Context, saveSettings bool) error {
	active := make([]ManagedBackend, 0, len(records))
	for _, record := range records {
		if record.Enabled {
			active = append(active, record)
		}
	}
	if len(active) == 0 {
		return errors.New("at least one backend must remain enabled")
	}
	if len(active) > 50 {
		return errors.New("backend pool cannot contain more than 50 nodes")
	}
	existing := make(map[string]*BackendNode)
	for _, node := range pool.Nodes() {
		if node.configID != "" {
			existing[node.configID] = node
		}
	}
	nodes := make([]*BackendNode, 0, len(active))
	for _, record := range active {
		target, err := url.Parse(record.URL)
		if err != nil {
			return err
		}
		node := existing[record.ID]
		if node == nil || node.URL().String() != target.String() || node.healthPath != record.HealthPath {
			node = pool.NewNode(target)
			node.configID = record.ID
			node.healthPath = record.HealthPath
		}
		node.weight.Store(record.Weight)
		node.maintenance.Store(record.Maintenance)
		nodes = append(nodes, node)
	}
	if err := pool.ReplaceNodes(nodes); err != nil {
		return err
	}
	weights := make(map[string]int64, len(nodes))
	for _, node := range nodes {
		weights[node.Name()] = pool.Weight(node)
	}
	if saveSettings {
		return store.SaveLoadBalancingSettings(ctx, LoadBalancingSettings{Strategy: pool.Strategy(), Weights: weights})
	}
	return nil
}

func decodeRouteInput(w http.ResponseWriter, r *http.Request, creating bool, current ManagedRoute) (ManagedRoute, int, error) {
	var input struct {
		Name              string   `json:"name"`
		PathPrefix        string   `json:"pathPrefix"`
		Methods           []string `json:"methods"`
		BackendID         string   `json:"backendId"`
		AuthRequired      *bool    `json:"authRequired"`
		Enabled           *bool    `json:"enabled"`
		RequestsPerWindow *int     `json:"requestsPerWindow"`
		WindowSeconds     *int     `json:"windowSeconds"`
		Burst             *int     `json:"burst"`
	}
	if err := decodeManagementJSON(w, r, &input); err != nil {
		return ManagedRoute{}, http.StatusBadRequest, errors.New("invalid JSON request")
	}
	route := current
	if input.Name != "" || creating {
		route.Name = strings.TrimSpace(input.Name)
	}
	if input.PathPrefix != "" || creating {
		route.PathPrefix = input.PathPrefix
	}
	if len(input.Methods) > 0 || creating {
		route.Methods = input.Methods
	}
	if input.BackendID != "" || creating {
		route.BackendID = input.BackendID
	}
	if input.AuthRequired != nil {
		route.AuthRequired = *input.AuthRequired
	} else if creating {
		route.AuthRequired = true
	}
	if input.Enabled != nil {
		route.Enabled = *input.Enabled
	} else if creating {
		route.Enabled = true
	}
	if input.RequestsPerWindow != nil {
		route.RequestsPerWindow = *input.RequestsPerWindow
	} else if creating {
		route.RequestsPerWindow = 0
	}
	if input.WindowSeconds != nil {
		route.WindowSeconds = *input.WindowSeconds
	} else if creating {
		route.WindowSeconds = 0
	}
	if input.Burst != nil {
		route.Burst = *input.Burst
	} else if creating {
		route.Burst = 0
	}
	if route.Name == "" || len(route.Name) > 100 {
		return ManagedRoute{}, http.StatusBadRequest, errors.New("route name is required and limited to 100 characters")
	}
	if err := validateRoutePath(route.PathPrefix); err != nil {
		return ManagedRoute{}, http.StatusBadRequest, err
	}
	if !uuidPattern.MatchString(route.BackendID) {
		return ManagedRoute{}, http.StatusBadRequest, errors.New("backendId must be a valid backend ID")
	}
	if len(route.Methods) == 0 || len(route.Methods) > 7 {
		return ManagedRoute{}, http.StatusBadRequest, errors.New("route must allow between 1 and 7 HTTP methods")
	}
	methods := make([]string, 0, len(route.Methods))
	seen := make(map[string]struct{}, len(route.Methods))
	for _, method := range route.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if !validRouteMethod(method) {
			return ManagedRoute{}, http.StatusBadRequest, fmt.Errorf("unsupported HTTP method %q", method)
		}
		if _, exists := seen[method]; exists {
			continue
		}
		seen[method] = struct{}{}
		methods = append(methods, method)
	}
	if _, wildcard := seen["*"]; wildcard && len(methods) != 1 {
		return ManagedRoute{}, http.StatusBadRequest, errors.New("wildcard method cannot be combined with specific methods")
	}
	route.Methods = methods
	if (route.RequestsPerWindow == 0) != (route.WindowSeconds == 0) || route.RequestsPerWindow < 0 || route.RequestsPerWindow > 1_000_000 || route.WindowSeconds < 0 || route.WindowSeconds > 86_400 || route.Burst < 0 || route.Burst > 1_000_000 || (route.RequestsPerWindow == 0 && route.Burst > 0) {
		return ManagedRoute{}, http.StatusBadRequest, errors.New("route rate limit must be disabled with all fields zero or set to 1..1000000 requests per 1..86400 seconds and burst 0..1000000")
	}
	return route, 0, nil
}

func validateRouteBackend(route ManagedRoute, pool *BackendPool) error {
	if !route.Enabled {
		return nil
	}
	if pool == nil || pool.NodeByConfigID(route.BackendID) == nil {
		return errors.New("route must reference an enabled backend")
	}
	return nil
}

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func auditResource(path string) (string, string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "management", ""
	}
	resource := parts[1]
	if resource == "settings" && len(parts) > 2 {
		return "setting", parts[2]
	}
	if len(parts) > 2 {
		return strings.TrimSuffix(resource, "s"), parts[2]
	}
	return strings.TrimSuffix(resource, "s"), ""
}

func decodeManagementJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

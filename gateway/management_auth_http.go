package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var dummyDashboardPasswordHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

func registerAuthenticationRoutes(mux *http.ServeMux, auth *ManagementAuth) {
	if auth == nil || auth.store == nil {
		return
	}
	mux.HandleFunc("/api/auth/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			count, err := auth.store.CountDashboardUsers(r.Context())
			if err != nil {
				http.Error(w, "Unable to check dashboard setup", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"setupRequired": count == 0})
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		provided := r.Header.Get("X-API-Key")
		if auth.bootstrapKey == "" || provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(auth.bootstrapKey)) != 1 {
			http.Error(w, "Bootstrap API key required", http.StatusUnauthorized)
			return
		}
		clientIP := remoteClientIdentity(r.RemoteAddr, r.Header.Get("X-Real-IP"))
		if !auth.AllowLoginAttempt(r.Context(), clientIP) {
			http.Error(w, "Too many setup attempts; try again later", http.StatusTooManyRequests)
			return
		}
		var input struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeManagementJSON(w, r, &input); err != nil {
			http.Error(w, "Invalid setup request", http.StatusBadRequest)
			return
		}
		email, err := normalizeDashboardCredentials(input.Email, input.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		hash, err := hashDashboardPassword(input.Password)
		if err != nil {
			http.Error(w, "Unable to prepare administrator credentials", http.StatusInternalServerError)
			return
		}
		user, err := auth.store.BootstrapDashboardAdmin(r.Context(), email, hash)
		if errors.Is(err, ErrDashboardSetupComplete) {
			http.Error(w, "Dashboard setup is already complete", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "Unable to create dashboard administrator", http.StatusConflict)
			return
		}
		auth.LoginSucceeded(clientIP)
		issueDashboardSession(w, r, auth, user)
	})

	mux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		clientIP := remoteClientIdentity(r.RemoteAddr, r.Header.Get("X-Real-IP"))
		if !auth.AllowLoginAttempt(r.Context(), clientIP) {
			http.Error(w, "Too many account creation attempts; try again later", http.StatusTooManyRequests)
			return
		}
		var input struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeManagementJSON(w, r, &input); err != nil {
			http.Error(w, "Invalid account request", http.StatusBadRequest)
			return
		}
		email, err := normalizeDashboardCredentials(input.Email, input.Password)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		hash, err := hashDashboardPassword(input.Password)
		if err != nil {
			http.Error(w, "Unable to prepare account credentials", http.StatusInternalServerError)
			return
		}
		user, err := auth.store.RegisterDashboardUser(r.Context(), email, hash)
		if errors.Is(err, ErrDashboardSetupRequired) {
			http.Error(w, "Dashboard administrator setup is required before account registration", http.StatusConflict)
			return
		}
		if err != nil {
			http.Error(w, "Account could not be created; the email may already be registered", http.StatusConflict)
			return
		}
		auth.LoginSucceeded(clientIP)
		issueDashboardSession(w, r, auth, user)
	})

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		clientIP := remoteClientIdentity(r.RemoteAddr, r.Header.Get("X-Real-IP"))
		if !auth.AllowLoginAttempt(r.Context(), clientIP) {
			http.Error(w, "Too many login attempts; try again later", http.StatusTooManyRequests)
			return
		}
		var input struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := decodeManagementJSON(w, r, &input); err != nil {
			http.Error(w, "Invalid login request", http.StatusBadRequest)
			return
		}
		email := strings.ToLower(strings.TrimSpace(input.Email))
		user, storedHash, err := auth.store.dashboardUserByEmail(r.Context(), email)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				_ = bcrypt.CompareHashAndPassword(dummyDashboardPasswordHash, []byte(input.Password))
				http.Error(w, "Invalid email or password", http.StatusUnauthorized)
				return
			}
			http.Error(w, "Authentication service unavailable", http.StatusServiceUnavailable)
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(input.Password)) != nil {
			http.Error(w, "Invalid email or password", http.StatusUnauthorized)
			return
		}
		auth.LoginSucceeded(clientIP)
		issueDashboardSession(w, r, auth, user)
	})

	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		principal, err := auth.AuthenticateSession(r)
		if err != nil {
			http.Error(w, "Authentication required", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, principal)
	})

	mux.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
			return
		}
		if cookie, err := r.Cookie(dashboardSessionCookie); err == nil {
			if token, decodeErr := base64.RawURLEncoding.DecodeString(cookie.Value); decodeErr == nil && len(token) == 32 {
				sum := sha256.Sum256(token)
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				defer cancel()
				if err := auth.store.DeleteDashboardSession(ctx, sum[:]); err != nil {
					http.Error(w, "Unable to end dashboard session", http.StatusServiceUnavailable)
					return
				}
			}
		}
		auth.ClearSessionCookie(w)
		w.WriteHeader(http.StatusNoContent)
	})
}

func normalizeDashboardCredentials(email, password string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || len(email) > 254 {
		return "", errors.New("enter a valid email address")
	}
	if len(password) < 12 || len(password) > 72 {
		return "", errors.New("password must contain between 12 and 72 UTF-8 bytes")
	}
	return email, nil
}

func issueDashboardSession(w http.ResponseWriter, r *http.Request, auth *ManagementAuth, user DashboardUser) {
	token, err := auth.CreateSession(r.Context(), user)
	if err != nil {
		http.Error(w, "Unable to create dashboard session", http.StatusServiceUnavailable)
		return
	}
	auth.SetSessionCookie(w, token)
	writeJSON(w, http.StatusOK, ManagementPrincipal{ID: user.ID, Email: user.Email, Role: user.Role})
}

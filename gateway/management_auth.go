package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const dashboardSessionCookie = "gatekeeper_session"
const dashboardSessionLifetime = 12 * time.Hour

type ManagementPrincipal struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type ManagementAuth struct {
	store         *ManagementStore
	bootstrapKey  string
	secureCookie  bool
	limiter       RateLimiter
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

type loginAttempt struct {
	count int
	reset time.Time
}

func NewManagementAuth(store *ManagementStore, bootstrapKey string, secureCookie bool, limiter RateLimiter) *ManagementAuth {
	return &ManagementAuth{
		store: store, bootstrapKey: bootstrapKey, secureCookie: secureCookie, limiter: limiter,
		loginAttempts: make(map[string]loginAttempt),
	}
}

func hashDashboardPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func compareDashboardPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func (a *ManagementAuth) Authenticate(r *http.Request) (ManagementPrincipal, error) {
	if a == nil || a.store == nil {
		return ManagementPrincipal{}, errors.New("management authentication is unavailable")
	}
	return a.AuthenticateSession(r)
}

func (a *ManagementAuth) AuthenticateSession(r *http.Request) (ManagementPrincipal, error) {
	cookie, err := r.Cookie(dashboardSessionCookie)
	if err != nil {
		return ManagementPrincipal{}, errors.New("authentication required")
	}
	token, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(token) != 32 {
		return ManagementPrincipal{}, errors.New("invalid session")
	}
	sum := sha256.Sum256(token)
	user, err := a.store.DashboardSessionUser(r.Context(), sum[:])
	if err != nil {
		return ManagementPrincipal{}, err
	}
	return ManagementPrincipal{ID: user.ID, Email: user.Email, Role: user.Role}, nil
}

func (a *ManagementAuth) CanRead(principal ManagementPrincipal) bool {
	return principal.Role == "admin" || principal.Role == "developer" || principal.Role == "viewer"
}

func (a *ManagementAuth) CanManage(principal ManagementPrincipal, path string) bool {
	if principal.Role == "admin" {
		return true
	}
	if principal.Role != "developer" {
		return false
	}
	for _, prefix := range []string{"/api/applications", "/api/keys", "/api/routes", "/api/settings/load-balancing"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (a *ManagementAuth) AllowLoginAttempt(ctx context.Context, clientIP string) bool {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	now := time.Now()
	for ip, attempt := range a.loginAttempts {
		if now.After(attempt.reset) {
			delete(a.loginAttempts, ip)
		}
	}
	attempt := a.loginAttempts[clientIP]
	if now.After(attempt.reset) {
		attempt = loginAttempt{reset: now.Add(15 * time.Minute)}
	}
	if attempt.count >= 10 {
		return false
	}
	if _, exists := a.loginAttempts[clientIP]; !exists && len(a.loginAttempts) >= 10000 {
		var oldestIP string
		var oldest time.Time
		for ip, candidate := range a.loginAttempts {
			if oldestIP == "" || candidate.reset.Before(oldest) {
				oldestIP, oldest = ip, candidate.reset
			}
		}
		delete(a.loginAttempts, oldestIP)
	}
	if a.limiter != nil {
		result, err := a.limiter.Allow(ctx, "dashboard-login:"+hashAPIKey(clientIP))
		if err != nil || !result.Allowed {
			return false
		}
	}
	attempt.count++
	a.loginAttempts[clientIP] = attempt
	return true
}

func (a *ManagementAuth) LoginSucceeded(clientIP string) {
	a.loginMu.Lock()
	delete(a.loginAttempts, clientIP)
	a.loginMu.Unlock()
}

func (a *ManagementAuth) CreateSession(ctx context.Context, user DashboardUser) (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	sum := sha256.Sum256(token[:])
	if err := a.store.CreateDashboardSession(ctx, user.ID, sum[:], time.Now().Add(dashboardSessionLifetime)); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func (a *ManagementAuth) SetSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: dashboardSessionCookie, Value: token, Path: "/", HttpOnly: true,
		Secure: a.secureCookie, SameSite: http.SameSiteStrictMode,
		Expires: time.Now().Add(dashboardSessionLifetime), MaxAge: int(dashboardSessionLifetime.Seconds()),
	})
}

func (a *ManagementAuth) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: dashboardSessionCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: a.secureCookie, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
}

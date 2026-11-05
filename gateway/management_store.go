package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var managementMigrations embed.FS

var ErrAPIKeyNotFound = errors.New("API key not found or inactive")
var ErrBackendNotFound = errors.New("backend not found")
var ErrRouteNotFound = errors.New("route not found")
var ErrDashboardSetupComplete = errors.New("dashboard administrator is already configured")

type ManagementStore struct {
	db *sql.DB
}

type ApplicationRecord struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
}

type APIKeyRecord struct {
	ID                string     `json:"id"`
	ApplicationID     string     `json:"applicationId"`
	ApplicationName   string     `json:"applicationName"`
	Name              string     `json:"name"`
	Prefix            string     `json:"prefix"`
	Permissions       []string   `json:"permissions"`
	RequestsPerWindow int        `json:"requestsPerWindow"`
	WindowSeconds     int        `json:"windowSeconds"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	RevokedAt         *time.Time `json:"revokedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	LastUsedAt        *time.Time `json:"lastUsedAt,omitempty"`
	UsageCount        int64      `json:"usageCount"`
	Secret            string     `json:"secret,omitempty"`
}

type APIKeyPrincipal struct {
	ID                string
	RequestsPerWindow int
	WindowSeconds     int
	Permissions       []string
}

type LoadBalancingSettings struct {
	Strategy LoadBalancingStrategy `json:"strategy"`
	Weights  map[string]int64      `json:"weights"`
}

type ManagedBackend struct {
	ID          string    `json:"configId"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	Weight      int64     `json:"weight"`
	Enabled     bool      `json:"enabled"`
	Maintenance bool      `json:"maintenance"`
	HealthPath  string    `json:"healthPath"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ManagedRoute struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	PathPrefix        string    `json:"pathPrefix"`
	Methods           []string  `json:"methods"`
	BackendID         string    `json:"backendId"`
	BackendName       string    `json:"backendName"`
	AuthRequired      bool      `json:"authRequired"`
	Enabled           bool      `json:"enabled"`
	RequestsPerWindow int       `json:"requestsPerWindow,omitempty"`
	WindowSeconds     int       `json:"windowSeconds,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type AuditLogRecord struct {
	ID           int64          `json:"id"`
	Actor        string         `json:"actor"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resourceType"`
	ResourceID   string         `json:"resourceId"`
	Details      map[string]any `json:"details"`
	CreatedAt    time.Time      `json:"createdAt"`
}

type DashboardUser struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

func NewManagementStore(db *sql.DB) *ManagementStore {
	if db == nil {
		return nil
	}
	return &ManagementStore{db: db}
}

func (s *ManagementStore) CountDashboardUsers(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count)
	return count, err
}

func (s *ManagementStore) CreateDashboardUser(ctx context.Context, email, passwordHash, role string) (DashboardUser, error) {
	var user DashboardUser
	err := s.db.QueryRowContext(ctx, `INSERT INTO users(email, password_hash, role)
		VALUES ($1, $2, $3) RETURNING id::text, email, role, created_at`, email, passwordHash, role).
		Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	return user, err
}

func (s *ManagementStore) BootstrapDashboardAdmin(ctx context.Context, email, passwordHash string) (DashboardUser, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DashboardUser{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(72184732011002)`); err != nil {
		return DashboardUser{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return DashboardUser{}, err
	}
	if count != 0 {
		return DashboardUser{}, ErrDashboardSetupComplete
	}
	var user DashboardUser
	err = tx.QueryRowContext(ctx, `INSERT INTO users(email, password_hash, role) VALUES ($1, $2, 'admin')
		RETURNING id::text, email, role, created_at`, email, passwordHash).
		Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if err != nil {
		return DashboardUser{}, err
	}
	if err := tx.Commit(); err != nil {
		return DashboardUser{}, err
	}
	return user, nil
}

func (s *ManagementStore) ListDashboardUsers(ctx context.Context) ([]DashboardUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, email, role, created_at FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]DashboardUser, 0)
	for rows.Next() {
		var user DashboardUser
		if err := rows.Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *ManagementStore) UpdateDashboardUserRole(ctx context.Context, id, role string) (DashboardUser, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DashboardUser{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(72184732011002)`); err != nil {
		return DashboardUser{}, err
	}
	var currentRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&currentRole); err != nil {
		return DashboardUser{}, err
	}
	if currentRole == "admin" && role != "admin" {
		var admins int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&admins); err != nil {
			return DashboardUser{}, err
		}
		if admins <= 1 {
			return DashboardUser{}, errors.New("cannot demote the last administrator")
		}
	}
	var user DashboardUser
	err = tx.QueryRowContext(ctx, `UPDATE users SET role = $2, updated_at = now() WHERE id = $1
		RETURNING id::text, email, role, created_at`, id, role).Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if err != nil {
		return DashboardUser{}, err
	}
	if err := tx.Commit(); err != nil {
		return DashboardUser{}, err
	}
	return user, nil
}

func (s *ManagementStore) DeleteDashboardUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(72184732011002)`); err != nil {
		return err
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = $1 FOR UPDATE`, id).Scan(&role); err != nil {
		return err
	}
	if role == "admin" {
		var admins int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE role = 'admin'`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return errors.New("cannot remove the last administrator")
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *ManagementStore) dashboardUserByEmail(ctx context.Context, email string) (DashboardUser, string, error) {
	var user DashboardUser
	var passwordHash string
	err := s.db.QueryRowContext(ctx, `SELECT id::text, email, role, created_at, password_hash FROM users WHERE email = $1`, email).
		Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt, &passwordHash)
	return user, passwordHash, err
}

func (s *ManagementStore) CreateDashboardSession(ctx context.Context, userID string, tokenHash []byte, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO dashboard_sessions(token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, tokenHash, userID, expiresAt)
	return err
}

func (s *ManagementStore) DashboardSessionUser(ctx context.Context, tokenHash []byte) (DashboardUser, error) {
	var user DashboardUser
	err := s.db.QueryRowContext(ctx, `UPDATE dashboard_sessions AS session SET last_seen_at = now()
		FROM users WHERE session.token_hash = $1 AND session.user_id = users.id AND session.expires_at > now()
		RETURNING users.id::text, users.email, users.role, users.created_at`, tokenHash).
		Scan(&user.ID, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DashboardUser{}, errors.New("session not found or expired")
	}
	return user, err
}

func (s *ManagementStore) DeleteDashboardSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM dashboard_sessions WHERE token_hash = $1`, tokenHash)
	return err
}

func (s *ManagementStore) CreateApplication(ctx context.Context, name, description string) (ApplicationRecord, error) {
	var app ApplicationRecord
	err := s.db.QueryRowContext(ctx, `INSERT INTO applications(name, description)
		VALUES ($1, $2) RETURNING id::text, name, description, created_at`, name, description).
		Scan(&app.ID, &app.Name, &app.Description, &app.CreatedAt)
	return app, err
}

func (s *ManagementStore) ListApplications(ctx context.Context) ([]ApplicationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, name, description, created_at
		FROM applications ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	apps := make([]ApplicationRecord, 0)
	for rows.Next() {
		var app ApplicationRecord
		if err := rows.Scan(&app.ID, &app.Name, &app.Description, &app.CreatedAt); err != nil {
			return nil, err
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

func (s *ManagementStore) ListBackends(ctx context.Context) ([]ManagedBackend, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id::text, name, url, weight, enabled, maintenance, health_path, created_at, updated_at
		FROM backends ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	backends := make([]ManagedBackend, 0)
	for rows.Next() {
		var backend ManagedBackend
		if err := rows.Scan(&backend.ID, &backend.Name, &backend.URL, &backend.Weight, &backend.Enabled, &backend.Maintenance,
			&backend.HealthPath, &backend.CreatedAt, &backend.UpdatedAt); err != nil {
			return nil, err
		}
		backends = append(backends, backend)
	}
	return backends, rows.Err()
}

func (s *ManagementStore) CreateBackend(ctx context.Context, backend ManagedBackend) (ManagedBackend, error) {
	err := s.db.QueryRowContext(ctx, `INSERT INTO backends(name, url, weight, enabled, maintenance, health_path)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, name, url, weight, enabled, maintenance, health_path, created_at, updated_at`,
		backend.Name, backend.URL, backend.Weight, backend.Enabled, backend.Maintenance, backend.HealthPath).
		Scan(&backend.ID, &backend.Name, &backend.URL, &backend.Weight, &backend.Enabled, &backend.Maintenance,
			&backend.HealthPath, &backend.CreatedAt, &backend.UpdatedAt)
	return backend, err
}

func (s *ManagementStore) CreateBackends(ctx context.Context, backends []ManagedBackend) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, backend := range backends {
		if _, err := tx.ExecContext(ctx, `INSERT INTO backends(name, url, weight, enabled, maintenance, health_path)
			VALUES ($1, $2, $3, $4, $5, $6)`, backend.Name, backend.URL, backend.Weight,
			backend.Enabled, backend.Maintenance, backend.HealthPath); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *ManagementStore) UpdateBackend(ctx context.Context, id string, backend ManagedBackend) (ManagedBackend, error) {
	err := s.db.QueryRowContext(ctx, `UPDATE backends SET name = $2, url = $3, weight = $4, enabled = $5,
		maintenance = $6, health_path = $7, updated_at = now() WHERE id = $1
		RETURNING id::text, name, url, weight, enabled, maintenance, health_path, created_at, updated_at`,
		id, backend.Name, backend.URL, backend.Weight, backend.Enabled, backend.Maintenance, backend.HealthPath).
		Scan(&backend.ID, &backend.Name, &backend.URL, &backend.Weight, &backend.Enabled, &backend.Maintenance,
			&backend.HealthPath, &backend.CreatedAt, &backend.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedBackend{}, ErrBackendNotFound
	}
	return backend, err
}

func (s *ManagementStore) DeleteBackend(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM backends WHERE id = $1`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrBackendNotFound
	}
	return nil
}

func (s *ManagementStore) RestoreBackend(ctx context.Context, backend ManagedBackend) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO backends(id, name, url, weight, enabled, maintenance, health_path, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, backend.ID, backend.Name, backend.URL, backend.Weight,
		backend.Enabled, backend.Maintenance, backend.HealthPath, backend.CreatedAt, backend.UpdatedAt)
	return err
}

func (s *ManagementStore) ListRoutes(ctx context.Context) ([]ManagedRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id::text, r.name, r.path_prefix, to_json(r.methods),
		r.backend_id::text, b.name, r.auth_required, r.enabled, r.created_at, r.updated_at,
		COALESCE(policy.requests, 0), COALESCE(policy.window_seconds, 0)
		FROM routes r JOIN backends b ON b.id = r.backend_id
		LEFT JOIN LATERAL (
			SELECT requests, window_seconds FROM rate_limit_rules
			WHERE route_id = r.id AND api_key_id IS NULL AND enabled
			ORDER BY id DESC LIMIT 1
		) policy ON TRUE
		ORDER BY r.path_prefix`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	routes := make([]ManagedRoute, 0)
	for rows.Next() {
		var route ManagedRoute
		var methods []byte
		if err := rows.Scan(&route.ID, &route.Name, &route.PathPrefix, &methods, &route.BackendID, &route.BackendName,
			&route.AuthRequired, &route.Enabled, &route.CreatedAt, &route.UpdatedAt, &route.RequestsPerWindow, &route.WindowSeconds); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(methods, &route.Methods); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (s *ManagementStore) CreateRoute(ctx context.Context, route ManagedRoute) (ManagedRoute, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedRoute{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO routes(name, path_prefix, methods, backend_id, auth_required, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id::text, name, path_prefix, backend_id::text, auth_required, enabled, created_at, updated_at`,
		route.Name, route.PathPrefix, route.Methods, route.BackendID, route.AuthRequired, route.Enabled).
		Scan(&route.ID, &route.Name, &route.PathPrefix, &route.BackendID, &route.AuthRequired, &route.Enabled, &route.CreatedAt, &route.UpdatedAt)
	if err != nil {
		return ManagedRoute{}, err
	}
	if err := saveRouteRateLimit(ctx, tx, route); err != nil {
		return ManagedRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedRoute{}, err
	}
	return route, nil
}

func (s *ManagementStore) UpdateRoute(ctx context.Context, id string, route ManagedRoute) (ManagedRoute, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManagedRoute{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `UPDATE routes SET name = $2, path_prefix = $3, methods = $4,
		backend_id = $5, auth_required = $6, enabled = $7, updated_at = now() WHERE id = $1
		RETURNING id::text, name, path_prefix, backend_id::text, auth_required, enabled, created_at, updated_at`,
		id, route.Name, route.PathPrefix, route.Methods, route.BackendID, route.AuthRequired, route.Enabled).
		Scan(&route.ID, &route.Name, &route.PathPrefix, &route.BackendID, &route.AuthRequired, &route.Enabled, &route.CreatedAt, &route.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedRoute{}, ErrRouteNotFound
	}
	if err != nil {
		return ManagedRoute{}, err
	}
	if err := saveRouteRateLimit(ctx, tx, route); err != nil {
		return ManagedRoute{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManagedRoute{}, err
	}
	return route, nil
}

func saveRouteRateLimit(ctx context.Context, tx *sql.Tx, route ManagedRoute) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM rate_limit_rules WHERE route_id = $1 AND api_key_id IS NULL`, route.ID); err != nil {
		return err
	}
	if route.RequestsPerWindow == 0 && route.WindowSeconds == 0 {
		return nil
	}
	if route.RequestsPerWindow < 1 || route.RequestsPerWindow > 1_000_000 || route.WindowSeconds < 1 || route.WindowSeconds > 86_400 {
		return errors.New("route rate limit must be 1..1000000 requests per 1..86400 seconds")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO rate_limit_rules(name, route_id, requests, window_seconds, enabled)
		VALUES ($1, $2, $3, $4, TRUE)`, "route:"+route.PathPrefix, route.ID, route.RequestsPerWindow, route.WindowSeconds)
	return err
}

func (s *ManagementStore) DeleteRoute(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM routes WHERE id = $1`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrRouteNotFound
	}
	return nil
}

func (s *ManagementStore) CreateAPIKey(ctx context.Context, applicationID, name string, permissions []string, limit, windowSeconds int, expiresAt *time.Time) (APIKeyRecord, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return APIKeyRecord{}, fmt.Errorf("generate API key: %w", err)
	}
	secret := "gk_" + base64.RawURLEncoding.EncodeToString(raw[:])
	prefix := secret[:11]
	if len(permissions) == 0 {
		permissions = []string{"read"}
	}
	var key APIKeyRecord
	key.Secret = secret
	key.Permissions = permissions
	key.RequestsPerWindow = limit
	key.WindowSeconds = windowSeconds
	key.ExpiresAt = expiresAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return APIKeyRecord{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO api_keys(
		application_id, name, key_prefix, key_hash, permissions, requests_per_window, window_seconds, expires_at
	) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	RETURNING id::text, application_id::text, key_prefix, created_at`,
		applicationID, name, prefix, []byte(hashAPIKey(secret)), permissions, limit, windowSeconds, expiresAt,
	).Scan(&key.ID, &key.ApplicationID, &key.Prefix, &key.CreatedAt)
	if err != nil {
		return APIKeyRecord{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT name FROM applications WHERE id = $1`, key.ApplicationID).Scan(&key.ApplicationName)
	if err != nil {
		return APIKeyRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return APIKeyRecord{}, err
	}
	return key, nil
}

func (s *ManagementStore) ListAPIKeys(ctx context.Context) ([]APIKeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT k.id::text, k.application_id::text, a.name,
		k.name, k.key_prefix, to_json(k.permissions), k.requests_per_window, k.window_seconds,
		k.expires_at, k.revoked_at, k.created_at, k.last_used_at, k.usage_count
		FROM api_keys k JOIN applications a ON a.id = k.application_id
		ORDER BY k.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]APIKeyRecord, 0)
	for rows.Next() {
		var key APIKeyRecord
		var permissions []byte
		if err := rows.Scan(&key.ID, &key.ApplicationID, &key.ApplicationName, &key.Name, &key.Prefix,
			&permissions, &key.RequestsPerWindow, &key.WindowSeconds, &key.ExpiresAt,
			&key.RevokedAt, &key.CreatedAt, &key.LastUsedAt, &key.UsageCount); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(permissions, &key.Permissions); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func (s *ManagementStore) RevokeAPIKey(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at = now()
		WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

func (s *ManagementStore) UpdateAPIKeyPolicy(ctx context.Context, id string, permissions []string, limit, windowSeconds int, expiresAt *time.Time, updateExpiry bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE api_keys
		SET permissions = $2, requests_per_window = $3, window_seconds = $4,
			expires_at = CASE WHEN $6 THEN $5 ELSE expires_at END
		WHERE id = $1 AND revoked_at IS NULL`, id, permissions, limit, windowSeconds, expiresAt, updateExpiry)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

func (s *ManagementStore) LoadLoadBalancingSettings(ctx context.Context) (LoadBalancingSettings, bool, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT setting_value FROM gateway_settings WHERE setting_key = 'load_balancing'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return LoadBalancingSettings{}, false, nil
	}
	if err != nil {
		return LoadBalancingSettings{}, false, err
	}
	var settings LoadBalancingSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return LoadBalancingSettings{}, false, err
	}
	return settings, true, nil
}

func (s *ManagementStore) SaveLoadBalancingSettings(ctx context.Context, settings LoadBalancingSettings) error {
	raw, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gateway_settings(setting_key, setting_value)
		VALUES ('load_balancing', $1::jsonb)
		ON CONFLICT (setting_key) DO UPDATE SET setting_value = EXCLUDED.setting_value, updated_at = now()`, string(raw))
	return err
}

func (s *ManagementStore) RecordAuditEvent(ctx context.Context, actor, action, resourceType, resourceID string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_logs(actor, action, resource_type, resource_id, details)
		VALUES ($1, $2, $3, $4, $5::jsonb)`, actor, action, resourceType, resourceID, string(raw))
	return err
}

func (s *ManagementStore) ListAuditEvents(ctx context.Context, limit int) ([]AuditLogRecord, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, actor, action, resource_type, resource_id, details, created_at
		FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]AuditLogRecord, 0)
	for rows.Next() {
		var entry AuditLogRecord
		var details []byte
		if err := rows.Scan(&entry.ID, &entry.Actor, &entry.Action, &entry.ResourceType, &entry.ResourceID, &details, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(details, &entry.Details); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *ManagementStore) ValidateAPIKey(ctx context.Context, raw string) (APIKeyPrincipal, error) {
	var principal APIKeyPrincipal
	var permissions []byte
	err := s.db.QueryRowContext(ctx, `UPDATE api_keys SET last_used_at = now(), usage_count = usage_count + 1
		WHERE key_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
		RETURNING id::text, requests_per_window, window_seconds, to_json(permissions)`, []byte(hashAPIKey(raw))).
		Scan(&principal.ID, &principal.RequestsPerWindow, &principal.WindowSeconds, &permissions)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyPrincipal{}, ErrAPIKeyNotFound
	}
	if err == nil {
		err = json.Unmarshal(permissions, &principal.Permissions)
	}
	return principal, err
}

func normalizePermissions(values []string) ([]string, error) {
	allowed := map[string]bool{"read": true, "write": true}
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if !allowed[value] {
			return nil, fmt.Errorf("unsupported permission %q", value)
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	if len(result) == 0 {
		result = []string{"read"}
	}
	if seen["write"] && !seen["read"] {
		result = append([]string{"read"}, result...)
	}
	return result, nil
}

// OpenManagementDB opens PostgreSQL, verifies connectivity, and applies pending
// schema migrations before returning a usable database pool.
func OpenManagementDB(ctx context.Context, databaseURL string) (*sql.DB, error) {
	if databaseURL == "" {
		return nil, errors.New("database URL is required")
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open management database: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to management database: %w", err)
	}
	if err := applyManagementMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func applyManagementMigrations(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}

	entries, err := managementMigrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list management migrations: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return fmt.Errorf("migration filename %q must start with a numeric version", name)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version < 1 {
			return fmt.Errorf("migration filename %q must start with a positive numeric version", name)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(72184732011001)`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("lock migration runner: %w", err)
		}
		var applied bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&applied); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("check migration version %d: %w", version, err)
		}
		if applied {
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("commit migration %d: %w", version, err)
			}
			continue
		}
		script, err := managementMigrations.ReadFile("migrations/" + name)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func TestManagementMigrationsApplyAndAreIdempotent(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	db, err := OpenManagementDB(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open management database: %v", err)
	}
	defer db.Close()

	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name IN
		('users', 'applications', 'api_keys', 'backends', 'routes', 'rate_limit_rules', 'audit_logs', 'gateway_settings', 'dashboard_sessions')`).Scan(&tableCount); err != nil {
		t.Fatalf("query migrated tables: %v", err)
	}
	if tableCount != 9 {
		t.Fatalf("expected 9 management tables, found %d", tableCount)
	}

	if err := applyManagementMigrations(ctx, db); err != nil {
		t.Fatalf("reapply migrations: %v", err)
	}

	var migrationCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = 1`).Scan(&migrationCount); err != nil {
		t.Fatalf("query migration record: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("expected migration version 1 exactly once, found %d", migrationCount)
	}
	var thirdMigrationCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = 3`).Scan(&thirdMigrationCount); err != nil || thirdMigrationCount != 1 {
		t.Fatalf("expected dashboard session migration version 3 exactly once, found %d err=%v", thirdMigrationCount, err)
	}
	var secondMigrationCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = 2`).Scan(&secondMigrationCount); err != nil || secondMigrationCount != 1 {
		t.Fatalf("expected route policy migration version 2 exactly once, found %d err=%v", secondMigrationCount, err)
	}

	store := NewManagementStore(db)
	history := NewRequestLogger(2)
	stopHistory, err := startRequestHistory(store, history, "integration-history")
	if err != nil {
		t.Fatal(err)
	}
	history.Record(RequestLogEntry{ID: "oldest", Status: 200})
	history.Record(RequestLogEntry{ID: "retained", Status: 503})
	history.Record(RequestLogEntry{ID: "newest", Status: 200})
	stopHistory()
	restored := NewRequestLogger(2)
	stopRestored, err := startRequestHistory(store, restored, "integration-history")
	if err != nil {
		t.Fatal(err)
	}
	stopRestored()
	entries := restored.GetEntries(2, 0, "", "")
	if len(entries) != 2 || entries[0].ID != "newest" || entries[1].ID != "retained" {
		t.Fatalf("history restore ordering/bound: %+v", entries)
	}
	app, err := store.CreateApplication(ctx, "integration-"+time.Now().Format("150405.000000000"), "test application")
	if err != nil {
		t.Fatalf("create application: %v", err)
	}
	key, err := store.CreateAPIKey(ctx, app.ID, "read-only integration key", []string{"read"}, 5, 30, nil)
	if err != nil {
		t.Fatalf("create API key: %v", err)
	}
	if key.Secret == "" || !strings.HasPrefix(key.Secret, "gk_") {
		t.Fatalf("expected generated one-time secret, got %q", key.Secret)
	}
	principal, err := store.ValidateAPIKey(ctx, key.Secret)
	if err != nil {
		t.Fatalf("validate generated API key: %v", err)
	}
	if principal.ID != key.ID || principal.RequestsPerWindow != 5 || principal.WindowSeconds != 30 || len(principal.Permissions) != 1 || principal.Permissions[0] != "read" {
		t.Fatalf("unexpected validated key principal: %+v", principal)
	}
	listed, err := store.ListAPIKeys(ctx)
	if err != nil {
		t.Fatalf("list API keys: %v", err)
	}
	var found *APIKeyRecord
	for i := range listed {
		if listed[i].ID == key.ID {
			found = &listed[i]
			break
		}
	}
	if found == nil || found.Secret != "" || found.Prefix != key.Prefix || found.UsageCount != 1 {
		t.Fatalf("API key list leaked secret or omitted metadata: %+v", found)
	}

	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	target, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	node := NewBackendNode(target, nil, http.DefaultTransport)
	pool, err := NewBackendPool([]*BackendNode{node})
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapManagedBackends(ctx, store, pool); err != nil {
		t.Fatalf("bootstrap persisted backends: %v", err)
	}
	// A second independently constructed gateway must converge on management
	// changes from this instance without a process restart.
	peerURL, _ := url.Parse("http://127.0.0.1:9")
	peerPool, err := NewBackendPool([]*BackendNode{NewBackendNode(peerURL, nil, http.DefaultTransport)})
	if err != nil {
		t.Fatal(err)
	}
	peerRoutes := NewRouteRegistry(peerPool)
	stopSync := startConfigurationSync(store, peerPool, peerRoutes)
	t.Cleanup(stopSync)
	initialBackendCount := len(pool.Nodes())
	policyLimiter := &capturingPolicyLimiter{}
	adminEmail := "integration-" + time.Now().Format("150405.000000000") + "@example.test"
	adminPassword := "integration-admin-password"
	managementAuth := NewManagementAuth(store, testAPIKey, false, policyLimiter)
	handler := newGatewayHandlerWithOptions(testAPIKey, pool, policyLimiter, WithManagementStore(store), WithManagementAuth(managementAuth))
	setupRequest := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, adminEmail, adminPassword)))
	setupRequest.Header.Set("X-API-Key", testAPIKey)
	setupResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupResponse, setupRequest)
	if setupResponse.Code != http.StatusOK {
		t.Fatalf("dashboard setup status=%d: %s", setupResponse.Code, setupResponse.Body.String())
	}
	setupCookie := setupResponse.Result().Cookies()[0]
	var admin ManagementPrincipal
	if err := json.Unmarshal(setupResponse.Body.Bytes(), &admin); err != nil || admin.Role != "admin" {
		t.Fatalf("dashboard setup principal=%+v err=%v", admin, err)
	}
	adminSession := setupCookie.Value
	adminUser := DashboardUser{ID: admin.ID, Email: admin.Email, Role: admin.Role}
	setupAgain := httptest.NewRequest(http.MethodPost, "/api/auth/setup", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, adminEmail, adminPassword)))
	setupAgain.Header.Set("X-API-Key", testAPIKey)
	setupAgainResponse := httptest.NewRecorder()
	handler.ServeHTTP(setupAgainResponse, setupAgain)
	if setupAgainResponse.Code != http.StatusConflict {
		t.Fatalf("repeat dashboard setup status=%d, want 409", setupAgainResponse.Code)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, adminEmail, adminPassword)))
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("dashboard login status=%d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	meRequest := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meRequest.AddCookie(loginResponse.Result().Cookies()[0])
	meResponse := httptest.NewRecorder()
	handler.ServeHTTP(meResponse, meRequest)
	if meResponse.Code != http.StatusOK {
		t.Fatalf("dashboard /me status=%d: %s", meResponse.Code, meResponse.Body.String())
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	loginCookie := loginResponse.Result().Cookies()[0]
	logoutRequest.AddCookie(loginCookie)
	logoutResponse := httptest.NewRecorder()
	handler.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("dashboard logout status=%d: %s", logoutResponse.Code, logoutResponse.Body.String())
	}
	loggedOutRequest := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	loggedOutRequest.AddCookie(loginCookie)
	loggedOutResponse := httptest.NewRecorder()
	handler.ServeHTTP(loggedOutResponse, loggedOutRequest)
	if loggedOutResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked dashboard session status=%d, want 401", loggedOutResponse.Code)
	}
	publicMetrics := httptest.NewRecorder()
	handler.ServeHTTP(publicMetrics, httptest.NewRequest(http.MethodGet, "/api/metrics", nil))
	if publicMetrics.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dashboard metrics status=%d, want 401", publicMetrics.Code)
	}
	privateMetricsRequest := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	privateMetricsRequest.AddCookie(&http.Cookie{Name: dashboardSessionCookie, Value: adminSession})
	privateMetrics := httptest.NewRecorder()
	handler.ServeHTTP(privateMetrics, privateMetricsRequest)
	if privateMetrics.Code != http.StatusOK || !strings.Contains(privateMetrics.Body.String(), "gateway_circuit_breaker_state") {
		t.Fatalf("authenticated dashboard metrics status=%d: %s", privateMetrics.Code, privateMetrics.Body.String())
	}
	withAdminSession := func(req *http.Request) {
		req.AddCookie(&http.Cookie{Name: dashboardSessionCookie, Value: adminSession})
	}
	request := httptest.NewRequest(http.MethodGet, "/api/students", nil)
	request.Header.Set("X-API-Key", key.Secret)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("managed read request status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if policyLimiter.limit != 5 || policyLimiter.window != 30*time.Second || policyLimiter.identity == key.Secret {
		t.Fatalf("managed quota was not applied safely: %+v", policyLimiter)
	}
	writeRequest := httptest.NewRequest(http.MethodPost, "/api/students", strings.NewReader(`{"name":"blocked"}`))
	writeRequest.Header.Set("X-API-Key", key.Secret)
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusForbidden || backendCalls != 1 {
		t.Fatalf("read-only key write status=%d backend calls=%d", writeResponse.Code, backendCalls)
	}
	policyBody := `{"permissions":["write"],"requestsPerWindow":8,"windowSeconds":45}`
	policyRequest := httptest.NewRequest(http.MethodPatch, "/api/keys/"+key.ID, strings.NewReader(policyBody))
	withAdminSession(policyRequest)
	policyResponse := httptest.NewRecorder()
	handler.ServeHTTP(policyResponse, policyRequest)
	if policyResponse.Code != http.StatusNoContent {
		t.Fatalf("update policy status = %d: %s", policyResponse.Code, policyResponse.Body.String())
	}
	updatedPrincipal, err := store.ValidateAPIKey(ctx, key.Secret)
	if err != nil || updatedPrincipal.RequestsPerWindow != 8 || updatedPrincipal.WindowSeconds != 45 || len(updatedPrincipal.Permissions) != 2 {
		t.Fatalf("updated policy did not take effect immediately: principal=%+v err=%v", updatedPrincipal, err)
	}
	writeAfterPolicy := httptest.NewRecorder()
	handler.ServeHTTP(writeAfterPolicy, writeRequest)
	if writeAfterPolicy.Code != http.StatusOK || backendCalls != 2 {
		t.Fatalf("updated write policy status=%d backend calls=%d", writeAfterPolicy.Code, backendCalls)
	}
	settingsRequest := httptest.NewRequest(http.MethodGet, "/api/settings/load-balancing", nil)
	withAdminSession(settingsRequest)
	settingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(settingsResponse, settingsRequest)
	if settingsResponse.Code != http.StatusOK {
		t.Fatalf("load balancing settings GET status=%d: %s", settingsResponse.Code, settingsResponse.Body.String())
	}
	managedBackendCalls := 0
	managedBackendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		managedBackendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer managedBackendServer.Close()
	managedBackendBody := fmt.Sprintf(`{"name":%q,"url":%q,"weight":2,"enabled":true,"maintenance":false,"healthPath":"/health"}`, "integration-"+time.Now().Format("150405.000000000"), managedBackendServer.URL)
	addBackendRequest := httptest.NewRequest(http.MethodPost, "/api/backends", strings.NewReader(managedBackendBody))
	withAdminSession(addBackendRequest)
	addBackendResponse := httptest.NewRecorder()
	handler.ServeHTTP(addBackendResponse, addBackendRequest)
	if addBackendResponse.Code != http.StatusCreated || len(pool.Nodes()) != initialBackendCount+1 {
		t.Fatalf("backend create status=%d active nodes=%d: %s", addBackendResponse.Code, len(pool.Nodes()), addBackendResponse.Body.String())
	}
	var managedBackend ManagedBackend
	if err := json.Unmarshal(addBackendResponse.Body.Bytes(), &managedBackend); err != nil || managedBackend.ID == "" {
		t.Fatalf("decode created backend: backend=%+v err=%v", managedBackend, err)
	}
	waitFor(t, 6*time.Second, func() bool {
		for _, peerNode := range peerPool.Nodes() {
			if peerNode.configID == managedBackend.ID {
				return true
			}
		}
		return false
	}, "peer gateway to add the managed backend")
	maintenanceRequest := httptest.NewRequest(http.MethodPut, "/api/backends/"+managedBackend.ID, strings.NewReader(`{"maintenance":true}`))
	withAdminSession(maintenanceRequest)
	maintenanceResponse := httptest.NewRecorder()
	handler.ServeHTTP(maintenanceResponse, maintenanceRequest)
	if maintenanceResponse.Code != http.StatusOK {
		t.Fatalf("backend maintenance update status=%d: %s", maintenanceResponse.Code, maintenanceResponse.Body.String())
	}
	var selectedMaintenanceTest *BackendNode
	for _, candidate := range pool.Nodes() {
		if candidate.configID == managedBackend.ID {
			selectedMaintenanceTest = candidate
		}
	}
	if selectedMaintenanceTest == nil || !selectedMaintenanceTest.maintenance.Load() {
		t.Fatalf("backend maintenance was not applied to live pool")
	}
	waitFor(t, 6*time.Second, func() bool {
		for _, peerNode := range peerPool.Nodes() {
			if peerNode.configID == managedBackend.ID && peerNode.maintenance.Load() {
				return true
			}
		}
		return false
	}, "peer gateway to apply backend maintenance")
	backendsRequest := httptest.NewRequest(http.MethodGet, "/api/backends", nil)
	withAdminSession(backendsRequest)
	backendsResponse := httptest.NewRecorder()
	handler.ServeHTTP(backendsResponse, backendsRequest)
	if backendsResponse.Code != http.StatusOK || !strings.Contains(backendsResponse.Body.String(), `"maintenance":true`) {
		t.Fatalf("backend configuration GET status=%d: %s", backendsResponse.Code, backendsResponse.Body.String())
	}
	resumeRequest := httptest.NewRequest(http.MethodPut, "/api/backends/"+managedBackend.ID, strings.NewReader(`{"maintenance":false}`))
	withAdminSession(resumeRequest)
	resumeResponse := httptest.NewRecorder()
	handler.ServeHTTP(resumeResponse, resumeRequest)
	if resumeResponse.Code != http.StatusOK {
		t.Fatalf("backend resume status=%d: %s", resumeResponse.Code, resumeResponse.Body.String())
	}
	routeBody := fmt.Sprintf(`{"name":"integration route","pathPrefix":"/api/integration","methods":["GET"],"backendId":%q,"authRequired":false,"enabled":true,"requestsPerWindow":2,"windowSeconds":30}`, managedBackend.ID)
	createRouteRequest := httptest.NewRequest(http.MethodPost, "/api/routes", strings.NewReader(routeBody))
	withAdminSession(createRouteRequest)
	createRouteResponse := httptest.NewRecorder()
	handler.ServeHTTP(createRouteResponse, createRouteRequest)
	if createRouteResponse.Code != http.StatusCreated {
		t.Fatalf("route create status=%d: %s", createRouteResponse.Code, createRouteResponse.Body.String())
	}
	var managedRoute ManagedRoute
	if err := json.Unmarshal(createRouteResponse.Body.Bytes(), &managedRoute); err != nil || managedRoute.ID == "" {
		t.Fatalf("decode created route: route=%+v err=%v", managedRoute, err)
	}
	waitFor(t, 6*time.Second, func() bool {
		route, matched, allowed := peerRoutes.Match("/api/integration/items", http.MethodGet)
		return matched && allowed && route.ID == managedRoute.ID && route.BackendID == managedBackend.ID
	}, "peer gateway to install the managed route")
	publicRouteRequest := httptest.NewRequest(http.MethodGet, "/api/integration/items", nil)
	publicRouteResponse := httptest.NewRecorder()
	handler.ServeHTTP(publicRouteResponse, publicRouteRequest)
	if publicRouteResponse.Code != http.StatusOK || managedBackendCalls != 1 {
		t.Fatalf("public route dispatch status=%d backend calls=%d: %s", publicRouteResponse.Code, managedBackendCalls, publicRouteResponse.Body.String())
	}
	if policyLimiter.limit != 2 || policyLimiter.window != 30*time.Second || !strings.HasPrefix(policyLimiter.identity, "route:"+managedRoute.ID+":") {
		t.Fatalf("route traffic policy not enforced: %+v", policyLimiter)
	}
	updateRoutePolicyRequest := httptest.NewRequest(http.MethodPut, "/api/routes/"+managedRoute.ID, strings.NewReader(`{"requestsPerWindow":6,"windowSeconds":45}`))
	withAdminSession(updateRoutePolicyRequest)
	updateRoutePolicyResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateRoutePolicyResponse, updateRoutePolicyRequest)
	if updateRoutePolicyResponse.Code != http.StatusOK {
		t.Fatalf("route policy update status=%d: %s", updateRoutePolicyResponse.Code, updateRoutePolicyResponse.Body.String())
	}
	waitFor(t, 6*time.Second, func() bool {
		route, matched, _ := peerRoutes.Match("/api/integration/items", http.MethodGet)
		return matched && route.ID == managedRoute.ID && route.RequestsPerWindow == 6 && route.WindowSeconds == 45
	}, "peer gateway to apply the updated route policy")
	publicRouteAfterPolicyUpdate := httptest.NewRecorder()
	handler.ServeHTTP(publicRouteAfterPolicyUpdate, httptest.NewRequest(http.MethodGet, "/api/integration/items", nil))
	if publicRouteAfterPolicyUpdate.Code != http.StatusOK || policyLimiter.limit != 6 || policyLimiter.window != 45*time.Second || managedBackendCalls != 2 {
		t.Fatalf("updated route policy status=%d limiter=%+v backend calls=%d", publicRouteAfterPolicyUpdate.Code, policyLimiter, managedBackendCalls)
	}
	methodDeniedRequest := httptest.NewRequest(http.MethodPost, "/api/integration/items", nil)
	withAdminSession(methodDeniedRequest)
	methodDeniedResponse := httptest.NewRecorder()
	handler.ServeHTTP(methodDeniedResponse, methodDeniedRequest)
	if methodDeniedResponse.Code != http.StatusMethodNotAllowed || managedBackendCalls != 2 {
		t.Fatalf("route method restriction status=%d backend calls=%d", methodDeniedResponse.Code, managedBackendCalls)
	}
	disableRouteRequest := httptest.NewRequest(http.MethodPut, "/api/routes/"+managedRoute.ID, strings.NewReader(`{"enabled":false}`))
	withAdminSession(disableRouteRequest)
	disableRouteResponse := httptest.NewRecorder()
	handler.ServeHTTP(disableRouteResponse, disableRouteRequest)
	if disableRouteResponse.Code != http.StatusOK {
		t.Fatalf("route disable status=%d: %s", disableRouteResponse.Code, disableRouteResponse.Body.String())
	}
	waitFor(t, 6*time.Second, func() bool {
		_, matched, _ := peerRoutes.Match("/api/integration/items", http.MethodGet)
		return !matched
	}, "peer gateway to disable the managed route")
	disabledRouteRequest := httptest.NewRequest(http.MethodGet, "/api/integration/items", nil)
	disabledRouteResponse := httptest.NewRecorder()
	handler.ServeHTTP(disabledRouteResponse, disabledRouteRequest)
	if disabledRouteResponse.Code != http.StatusUnauthorized || managedBackendCalls != 2 {
		t.Fatalf("disabled route remained active status=%d backend calls=%d", disabledRouteResponse.Code, managedBackendCalls)
	}
	deleteRouteRequest := httptest.NewRequest(http.MethodDelete, "/api/routes/"+managedRoute.ID, nil)
	withAdminSession(deleteRouteRequest)
	deleteRouteResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteRouteResponse, deleteRouteRequest)
	if deleteRouteResponse.Code != http.StatusNoContent {
		t.Fatalf("route delete status=%d: %s", deleteRouteResponse.Code, deleteRouteResponse.Body.String())
	}
	deleteBackendRequest := httptest.NewRequest(http.MethodDelete, "/api/backends/"+managedBackend.ID, nil)
	withAdminSession(deleteBackendRequest)
	deleteBackendResponse := httptest.NewRecorder()
	handler.ServeHTTP(deleteBackendResponse, deleteBackendRequest)
	if deleteBackendResponse.Code != http.StatusNoContent || len(pool.Nodes()) != initialBackendCount {
		t.Fatalf("backend delete status=%d active nodes=%d: %s", deleteBackendResponse.Code, len(pool.Nodes()), deleteBackendResponse.Body.String())
	}
	settingsBody := fmt.Sprintf(`{"strategy":"weighted_round_robin","weights":{%q:3}}`, node.Name())
	updateSettingsRequest := httptest.NewRequest(http.MethodPut, "/api/settings/load-balancing", strings.NewReader(settingsBody))
	withAdminSession(updateSettingsRequest)
	updateSettingsResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateSettingsResponse, updateSettingsRequest)
	var currentNode *BackendNode
	for _, candidate := range pool.Nodes() {
		if candidate.Name() == node.Name() {
			currentNode = candidate
		}
	}
	if updateSettingsResponse.Code != http.StatusOK || pool.Strategy() != StrategyWeightedRoundRobin || currentNode == nil || pool.Weight(currentNode) != 3 {
		weight := int64(0)
		if currentNode != nil {
			weight = pool.Weight(currentNode)
		}
		t.Fatalf("persisted balancing update status=%d strategy=%s weight=%d: %s", updateSettingsResponse.Code, pool.Strategy(), weight, updateSettingsResponse.Body.String())
	}
	settings, foundSettings, err := store.LoadLoadBalancingSettings(ctx)
	if err != nil || !foundSettings || settings.Strategy != StrategyWeightedRoundRobin || settings.Weights[node.Name()] != 3 {
		t.Fatalf("load persisted balancing settings = %+v found=%v err=%v", settings, foundSettings, err)
	}
	waitFor(t, 6*time.Second, func() bool {
		var peerNode *BackendNode
		for _, candidate := range peerPool.Nodes() {
			if candidate.Name() == node.Name() {
				peerNode = candidate
				break
			}
		}
		return peerPool.Strategy() == StrategyWeightedRoundRobin && peerNode != nil && peerPool.Weight(peerNode) == 3
	}, "peer gateway to apply load-balancing settings")
	auditRequest := httptest.NewRequest(http.MethodGet, "/api/audit-logs?limit=10", nil)
	withAdminSession(auditRequest)
	auditResponse := httptest.NewRecorder()
	handler.ServeHTTP(auditResponse, auditRequest)
	if auditResponse.Code != http.StatusOK || strings.Contains(auditResponse.Body.String(), key.Secret) {
		t.Fatalf("audit log list status=%d or leaked a secret: %s", auditResponse.Code, auditResponse.Body.String())
	}
	var auditResult struct {
		Events []AuditLogRecord `json:"events"`
	}
	if err := json.Unmarshal(auditResponse.Body.Bytes(), &auditResult); err != nil {
		t.Fatalf("decode audit events: %v", err)
	}
	if len(auditResult.Events) == 0 || auditResult.Events[0].Actor != admin.Email || auditResult.Events[0].Action != http.MethodPut || auditResult.Events[0].ResourceType != "setting" {
		t.Fatalf("expected persisted, sanitized settings audit event, got %+v", auditResult.Events)
	}
	unauthorizedManagement := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedManagement, httptest.NewRequest(http.MethodGet, "/api/keys", nil))
	if unauthorizedManagement.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated key list status = %d, want 401", unauthorizedManagement.Code)
	}
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	adminRequest.Header.Set("X-API-Key", testAPIKey)
	adminResponse := httptest.NewRecorder()
	handler.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusUnauthorized {
		t.Fatalf("consumer bootstrap key unexpectedly accessed management API: status=%d", adminResponse.Code)
	}
	adminRequest = httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	withAdminSession(adminRequest)
	adminResponse = httptest.NewRecorder()
	handler.ServeHTTP(adminResponse, adminRequest)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin key list status = %d, want 200", adminResponse.Code)
	}
	if strings.Contains(adminResponse.Body.String(), key.Secret) || strings.Contains(adminResponse.Body.String(), `"secret"`) {
		t.Fatalf("API key list response exposed a secret: %s", adminResponse.Body.String())
	}
	createUserRequest := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"email":"developer@example.test","password":"developer-password","role":"developer"}`))
	withAdminSession(createUserRequest)
	createUserResponse := httptest.NewRecorder()
	handler.ServeHTTP(createUserResponse, createUserRequest)
	if createUserResponse.Code != http.StatusCreated {
		t.Fatalf("dashboard user create status=%d: %s", createUserResponse.Code, createUserResponse.Body.String())
	}
	developer, _, err := store.dashboardUserByEmail(ctx, "developer@example.test")
	if err != nil {
		t.Fatalf("load developer account: %v", err)
	}
	developerAuth := NewManagementAuth(store, testAPIKey, false, policyLimiter)
	developerSession, err := developerAuth.CreateSession(ctx, developer)
	if err != nil {
		t.Fatalf("create developer session: %v", err)
	}
	developerList := httptest.NewRecorder()
	developerListRequest := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	developerListRequest.AddCookie(&http.Cookie{Name: dashboardSessionCookie, Value: developerSession})
	handler.ServeHTTP(developerList, developerListRequest)
	if developerList.Code != http.StatusOK {
		t.Fatalf("developer read status=%d", developerList.Code)
	}
	developerUsers := httptest.NewRecorder()
	developerUsersRequest := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	developerUsersRequest.AddCookie(&http.Cookie{Name: dashboardSessionCookie, Value: developerSession})
	handler.ServeHTTP(developerUsers, developerUsersRequest)
	if developerUsers.Code != http.StatusForbidden {
		t.Fatalf("developer account listing status=%d, want 403", developerUsers.Code)
	}
	if _, err := store.UpdateDashboardUserRole(ctx, adminUser.ID, "developer"); err == nil {
		t.Fatal("last administrator was demoted")
	}
	if err := store.DeleteDashboardUser(ctx, adminUser.ID); err == nil {
		t.Fatal("last administrator was deleted")
	}
	createKeyBody := fmt.Sprintf(`{"applicationId":%q,"name":"managed endpoint key","permissions":["write"],"requestsPerWindow":12,"windowSeconds":30}`, app.ID)
	createKeyRequest := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(createKeyBody))
	withAdminSession(createKeyRequest)
	createKeyResponse := httptest.NewRecorder()
	handler.ServeHTTP(createKeyResponse, createKeyRequest)
	if createKeyResponse.Code != http.StatusCreated {
		t.Fatalf("management key create status = %d: %s", createKeyResponse.Code, createKeyResponse.Body.String())
	}
	var createdKey APIKeyRecord
	if err := json.Unmarshal(createKeyResponse.Body.Bytes(), &createdKey); err != nil || createdKey.Secret == "" {
		t.Fatalf("management key create did not return its one-time secret: %v", err)
	}
	if len(createdKey.Permissions) != 2 || createdKey.Permissions[0] != "read" || createdKey.Permissions[1] != "write" {
		t.Fatalf("write permission should include read permission: %v", createdKey.Permissions)
	}
	invalidPermissions := fmt.Sprintf(`{"applicationId":%q,"name":"invalid key","permissions":["admin"],"requestsPerWindow":12,"windowSeconds":30}`, app.ID)
	invalidRequest := httptest.NewRequest(http.MethodPost, "/api/keys", strings.NewReader(invalidPermissions))
	withAdminSession(invalidRequest)
	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, invalidRequest)
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid permission create status = %d, want 400", invalidResponse.Code)
	}

	if err := store.RevokeAPIKey(ctx, key.ID); err != nil {
		t.Fatalf("revoke API key: %v", err)
	}
	if _, err := store.ValidateAPIKey(ctx, key.Secret); err != ErrAPIKeyNotFound {
		t.Fatalf("revoked key validation error = %v, want ErrAPIKeyNotFound", err)
	}
}

type capturingPolicyLimiter struct {
	limit    int64
	window   time.Duration
	identity string
}

func (l *capturingPolicyLimiter) Allow(context.Context, string) (RateLimitResult, error) {
	return RateLimitResult{Allowed: true}, nil
}

func (l *capturingPolicyLimiter) AllowWithPolicy(_ context.Context, identity string, limit int64, window time.Duration) (RateLimitResult, error) {
	l.identity = identity
	l.limit = limit
	l.window = window
	return RateLimitResult{Allowed: true, Limit: limit, Remaining: limit - 1, RetryAfter: int64(window.Seconds())}, nil
}

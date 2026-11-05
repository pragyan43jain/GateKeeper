# GateKeeper audit and implementation progress

Date: 2026-11-05

## Source and working state

- Branch: `main`
- Local `HEAD`: `5a076eec9d3cbb97741ee934110329571f23afe7`
- `origin/main` at audit time: same SHA
- The checkout has pre-existing uncommitted changes in the gateway, backend, dashboard, and tests, plus untracked `ARCHITECTURAL_AUDIT.txt`, `gateway/features_test.go`, and `frontend/src/components/InspectorSection.tsx`. These were preserved and included in this audit.
- The root `ARCHITECTURAL_AUDIT.txt` is an older report and was left untouched.

## Existing architecture and verified capabilities

- Go `net/http` gateway with `httputil.ReverseProxy`, middleware, graceful shutdown, request body limit, and configurable upstream response-header timeout.
- Constant-time comparison against one environment-configured API key. The gateway strips `X-API-Key` before forwarding.
- Redis Lua-backed atomic fixed-window rate limiting, keyed by a SHA-256 digest. Redis errors fail closed by default.
- Round-robin backend pool, independent transport-level circuit breakers, and bounded upstream health probes.
- Prometheus request, latency, rate-limit, per-backend breaker, and health metrics. Prometheus query endpoints are scoped in Caddy; Grafana is separately proxied by host name.
- Existing local changes add a bounded in-memory request inspector, request IDs, backend diagnostics, health state reporting, and corresponding dashboard screens.
- React 19 + TypeScript + Vite dashboard has Overview, Upstream Nodes, Traffic Policies, Observability, Playground, Request Inspector, API Key Manager, Audit Logs, and Route Manager pages.
- Docker Compose includes Caddy, frontend, gateway, two sample backends, Redis, PostgreSQL, Prometheus, and Grafana. GitHub Actions runs `make check` with Redis and PostgreSQL.
- PostgreSQL 17 is internal to Compose. The gateway applies embedded, advisory-lock-protected schema migrations during startup.
- PostgreSQL-backed `/api/applications` and `/api/keys` endpoints provide application and API key creation, listing, expiration metadata, permission metadata, quota configuration, usage tracking, and revocation. Secrets are shown once and stored as hashes. Managed API keys use per-key Redis counters and enforce read/write permissions.
- Admins can patch managed key permissions and quotas; the gateway reads the updated settings during the next request.
- The dashboard now includes an API Key Manager with one-time secret display, key metadata, and confirmed revocation.
- The backend pool now supports round robin (default), weighted round robin, least in-flight proxied requests, and random selection. Health and breaker eligibility checks apply to every strategy; strategy and per-backend weight are visible through diagnostics.
- Authorized administrators can view and change balancing strategy and backend weights at runtime. Settings persist in PostgreSQL and are restored at gateway startup; updates are validated and atomically applied. The dashboard includes a strategy and weight editor.
- The Traffic Policies dashboard can update managed API-key permissions and quotas; those values are persisted and read on the next request.
- Mutating calls to protected management endpoints write sanitized administrative events into PostgreSQL; `/api/audit-logs` and the dashboard Audit Logs page expose the latest 200 events.
- Backend records are bootstrapped once from environment configuration, then loaded from PostgreSQL at startup. Protected APIs can add, update, disable, place nodes in maintenance, and remove them; pool membership swaps atomically and health checks observe the current set.
- Route records load into an atomic longest-prefix router at startup. Protected route APIs and the dashboard can configure backend association, methods, authentication, enable/disable, and deletion; route dispatch applies method restrictions and honors backend health and maintenance state.

## Validation

`make check` passes on the current implementation:

- `go test -race -count=1 ./...`
- `go vet ./...`
- Frontend production TypeScript/Vite build
- Oxlint
- 13 frontend service tests

The disposable PostgreSQL integration test was also run with `-race`; it verifies all eight management tables, migration idempotence, application/key lifecycle, secret omission from list responses, permission enforcement, dynamic policy changes, per-key quota propagation, revocation, runtime backend create/maintenance/delete, route creation/method/auth enforcement/disable/delete, persisted balancing settings, and sanitized audit history. Unit tests cover atomic pool replacement and longest-prefix route matching. The gateway Docker image builds. The Vite build reports a non-blocking JavaScript bundle size warning. `git diff --check` and `docker compose config` succeed.

## Gaps against the final product vision

- The Redis limiter is fixed-window, not sliding-window. Per-key policies are dynamic; route-specific rate-limit rules remain schema-only and are not applied.
- The bootstrap management endpoints still use the shared `GATEKEEPER_API_KEY`; dashboard login, user sessions, and role-based authorization are not implemented.
- API key management supports create/list/revoke and expiry validation, but rotation and API-key permission checks are currently read/write at the whole `/api/*` surface. Request-inspector history remains in-memory and is lost on restart.
- Rate-limit logs now omit the stable key digest, retaining only outcome, path, and relevant error/quota fields.
- Upstream responses cannot replace the gateway-assigned `X-Request-ID`; a regression test verifies the response and inspector record stay correlated.
- The initial migration creates the users, applications, backends, routes, API keys, rate-limit rules, audit logs, and gateway settings tables. The PostgreSQL integration test checks all eight tables and migration idempotence.
- API key management supports create/list/revoke and expiry validation; rotation remains unavailable. Request inspector history remains in-memory and is lost on restart.
- Load-balancing strategy and weights have environment defaults plus persisted runtime management through the protected settings endpoint and dashboard. Least-connections uses in-flight proxied request counts.
- Active health checks and request inspection history remain in-memory. Persistent request history is still needed.
- Backend membership and route rules now persist and affect runtime routing. Route-level traffic limits are not yet connected to `rate_limit_rules`.
- Management audit records are attributed to a shared `bootstrap_admin` actor because dashboard sessions and user RBAC do not exist yet. This does not provide individual accountability.
- No OpenTelemetry tracing, alert rules, or k6 suite. Dashboard login/RBAC and persistent request inspection remain incomplete.
- Caddy blocks public `/metrics` access; Prometheus scrapes the gateway directly over the internal Compose network. Prometheus query access through Caddy remains restricted to the GET query endpoints used by the dashboard.
- The React dashboard accepts a gateway key in client state to call protected diagnostics. This is a transitional shared-key model, not dashboard authentication.
- Render deployment uses separate public web services for the sample backends and a Vercel-specific CORS allowlist; this should be reviewed before treating it as a production topology.

## Implementation order from this baseline

1. Stabilize and verify the current gateway, health manager, and inspector without discarding existing local work.
2. Add explicit administrative authentication/authorization boundaries before expanding management APIs.
3. Implement persistent API key and application management on the new schema.
4. Add dynamic traffic control and balancing features behind tested configuration interfaces.
5. Complete observability, dashboard pages, and deployment hardening incrementally.

This report describes the audited baseline; it does not claim the full product vision is implemented.

# GateKeeper implementation and verification

Scope: the existing API gateway, read-only demonstration upstream, management dashboard,
and Docker stack. The student endpoint is sample data, not a persistent student management application.

## Implemented

- Retain round-robin routing and independent per-upstream transport circuit breakers; upgrade Redis limits to an atomic sorted-set sliding window using Redis server time.
- Reject unsupported upstream URLs and embedded credentials; rewrite upstream Host and strip gateway API credentials before forwarding.
- Reject empty authentication configuration and invalid rate-limit tuning; round fractional windows up to whole Redis TTL seconds.
- Preserve response status after streaming flush and informational HTTP responses.
- Add upstream HTTP timeouts and graceful termination.
- Populate dashboard backends from actual metric labels; bound fetch duration and prevent overlapping refreshes.
- Pass rate-limit configuration through Compose and align the frontend container with Node 24.
- Add regression tests, CI with Redis, Make targets, and an executable smoke check.
- Document the sliding-window algorithm and its fail-closed Redis policy.
- Add an internal PostgreSQL 17 service and advisory-lock-protected, versioned initial schema migrations for management entities.
- Add PostgreSQL-backed application and API key management endpoints. API key secrets are generated cryptographically, stored as SHA-256 digests, and returned once at creation.
- Enforce managed key expiry and read/write permissions; apply each key's request limit and time window to its Redis counter and track key usage in PostgreSQL.
- Allow authorized administrators to update an active API key's quota, window, and permissions; the gateway applies those changes on its next key validation.
- Add the dashboard API Key Manager with application creation, key issuance, one-time secret display, usage metadata, and confirmed revocation.
- Add PostgreSQL integration coverage to CI and regression coverage for request ID correlation and secret handling.
- Add configurable round robin, weighted round robin, least in-flight requests, and random backend selection while applying health and circuit-breaker eligibility to each strategy.
- Persist load-balancing strategy and weights in PostgreSQL; apply validated administrator updates atomically at runtime and restore them on startup.
- Allow administrators to update managed API-key quotas and permissions from the Traffic Policies dashboard.
- Record sanitized changes from protected management endpoints in PostgreSQL and expose a bounded audit history API and dashboard page.
- Block public `/metrics` access through Caddy while keeping Prometheus scraping on the internal Docker network.
- Bootstrap backend records from existing environment configuration once, load persistent backend membership at startup, and support live backend add/update/disable/maintenance/removal with atomic pool replacement.
- Persist route CRUD and load enabled rules into an atomic longest-prefix router with method allowlists, backend association, optional API-key authentication, and health-aware dispatch.
- Add Route Manager and upstream configuration controls to the existing dashboard.
- Persist route-specific quotas and apply them alongside consumer-key limits at request time.
- Add bcrypt-backed dashboard users, one-time first-admin setup, opaque hashed session tokens, HttpOnly/SameSite cookies, session logout, and admin/developer/viewer authorization on management APIs.
- Remove shared consumer API keys as dashboard login credentials after setup; require a dashboard session for persistent management endpoints.
- Add administrator user and role management, protect the last administrator from demotion/removal, and include login/logout controls in the console.
- Add a secure first-run dashboard setup/login screen and optional environment-based initial administrator creation.
- Add sliding-window boundary coverage for Redis rate limiting.
- Synchronize persisted backend, route, and load-balancing configuration across gateway replicas every two seconds, preserving the last active configuration during database errors and serializing updates with local management mutations.
- Add bounded burst allowances to API-key and route policies, persist and propagate them, and enforce the combined quota atomically in the existing Redis sliding-window script.
- Add opt-in bounded transport retries for body-replayable idempotent HTTP methods, preserving per-backend circuit-breaker accounting and request cancellation.
- Persist backend health state transitions and failure reasons with a 100-event per-backend retention bound; expose them through a protected API and the Upstream Nodes console.
- Emit JSON logs from both `slog` and the standard logger, and allow the Caddy ingress host ports to be overridden for isolated local acceptance deployments.
- Provision a Grafana Prometheus datasource and an operations dashboard backed by the gateway's actual request, latency, rate-limit, health, and breaker metrics.
- Record request totals by gateway-selected backend, status code, and method with a bounded backend identity label, and visualize backend distribution in Grafana.
- Merge bounded request-history snapshots from all gateway instances into the protected Inspector API, de-duplicating the serving replica's live ring against its last checkpoint.

## Repeatable validation

1. `make check` (Redis must listen on localhost:6379. Set `TEST_DATABASE_URL` to run the PostgreSQL integration test; CI provisions both services.)
2. `docker compose up -d --build --wait` after configuring `.env` from `.env.example`.
3. `GATEKEEPER_API_KEY=... ALLOW_LOCAL_TLS=1 make smoke` for local Caddy certificates.
   For a public domain, set `GATEKEEPER_URL=https://your-domain` and omit `ALLOW_LOCAL_TLS`.
4. On a disposable stack, run `GATEKEEPER_URL=https://localhost:18443 GATEKEEPER_INSECURE_TLS=1 DASHBOARD_ADMIN_EMAIL=... DASHBOARD_ADMIN_PASSWORD=... python3 scripts/acceptance.py` to verify the dashboard session, key lifecycle, backend distribution, key and route quotas, Inspector, health history, protected metrics, and logout through Caddy. The TLS override is only for Caddy's local development certificate.
5. Check dashboard tabs and use the playground to observe backend identity and 429 responses.

## Deployment requirements

Configure your own bootstrap `GATEKEEPER_API_KEY`, Redis password, PostgreSQL password,
Grafana administrator password, domain, and certificate email. Existing local secrets are not overwritten by the implementation.
Circuit breakers intentionally track transport failures, not upstream HTTP 5xx responses.
Redis failure intentionally returns 503. `/health` is process liveness, not dependency readiness.

## Verified in this workspace

- `make check` passes with `TEST_DATABASE_URL` set, including Go race tests, `go vet`, frontend build/lint, 14 frontend service tests, and the PostgreSQL integration test.
- Frontend TypeScript production build, lint, and all 14 service tests pass. The frontend build reports a non-blocking large-chunk warning.
- `docker compose config --quiet` and `docker compose build gateway` pass.
- Redis sliding-window boundary, quota, and concurrency tests pass against the configured local Redis service.
- PostgreSQL integration passes against a fresh disposable PostgreSQL 17 database, including all six migrations, first-admin setup, login/logout, role checks, protected metrics, and last-admin safeguards.
- PostgreSQL replica synchronization integration passes for backend add/maintenance, route create/policy/disable, and load-balancing strategy/weight updates. `make check` passes after these changes.
- Burst allowance checks pass against Redis; fresh PostgreSQL integration verifies API-key policy update and route policy persistence/replica propagation with burst values. Dashboard build, lint, and all 14 frontend tests pass.
- Retry transport tests pass for transient failures, unsafe-method suppression, and non-retry of upstream HTTP error responses.
- PostgreSQL integration verifies unhealthy, recovering, and healthy transition history, and the dashboard health-history API service passes its frontend test.
- A fresh PostgreSQL integration test verifies that Inspector history merges and orders snapshots from independent gateway instances and that the protected request API can search a peer-only record.
- A fresh isolated Compose deployment passes the expanded Caddy ingress acceptance, including key and route burst limits, protected health-history retrieval, and a live backend stop/recovery while healthy-peer traffic continues. Isolated resources are removed after the run.
- JSON logging regression coverage passes for both `slog` fields and legacy standard-library log messages.
- Grafana provisions the operations dashboard and reports seven panels through the isolated Caddy ingress; Prometheus returns real per-backend series, gateway container logs parse as JSON, and Compose accepts the dashboard/provisioning mounts.
- Clean PostgreSQL-backed `make check` passes after all committed changes, including Go race tests, migrations and management integration, `go vet`, frontend build/lint, and all 14 frontend tests. The frontend bundle-size notice is non-blocking.
- `docker compose config --quiet`, gateway Docker image build, and Caddy validation through the existing Caddy container pass. The application stack was not restarted; only the disposable test database container was started and removed.

## Latest acceptance and remaining work

- Inspector history now checkpoints to PostgreSQL every two seconds and restores its bounded ring on startup; restart retention and ordering pass PostgreSQL integration tests.
- OpenTelemetry OTLP export and parent-context propagation pass a collector integration test. Four Prometheus alert rules pass promtool validation. External alert delivery still requires an operator-configured receiver.
- The isolated Compose acceptance and k6 runs pass; measurements and limits are recorded in scripts/LOAD_TESTING.md. The k6 run is dominated by explicit throttling and is not a capacity benchmark.
- Verified key lifecycle, two-backend distribution, key and route throttling, Inspector correlation, protected metrics, logout, and active backend failure/recovery through isolated Compose ingress; Grafana dashboard provisioning and real backend series are verified too.

## Operational limits

- Prometheus alert rules are loaded, but notification delivery needs an operator-configured Alertmanager and receiver.
- Inspector history is bounded per gateway instance and aggregated from PostgreSQL snapshots; remote records can be up to two seconds stale. A crash can lose records since the last successful checkpoint.
- No public deployment or paid hosting resources were created. Run the documented checks and acceptance against the intended hosting provider before presenting its production behavior as verified.

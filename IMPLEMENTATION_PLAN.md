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

## Repeatable validation

1. `make check` (Redis must listen on localhost:6379. Set `TEST_DATABASE_URL` to run the PostgreSQL integration test; CI provisions both services.)
2. `docker compose up -d --build --wait` after configuring `.env` from `.env.example`.
3. `GATEKEEPER_API_KEY=... ALLOW_LOCAL_TLS=1 make smoke` for local Caddy certificates.
   For a public domain, set `GATEKEEPER_URL=https://your-domain` and omit `ALLOW_LOCAL_TLS`.
4. Check dashboard tabs and use the playground to observe backend identity and 429 responses.

## Deployment requirements

Configure your own bootstrap `GATEKEEPER_API_KEY`, Redis password, PostgreSQL password,
Grafana administrator password, domain, and certificate email. Existing local secrets are not overwritten by the implementation.
Circuit breakers intentionally track transport failures, not upstream HTTP 5xx responses.
Redis failure intentionally returns 503. `/health` is process liveness, not dependency readiness.

## Verified in this workspace

- `make check` passes with `TEST_DATABASE_URL` set, including Go race tests, `go vet`, frontend build/lint, 13 frontend service tests, and the PostgreSQL integration test.
- Frontend TypeScript production build, lint, and all 13 service tests pass. The frontend build reports a non-blocking large-chunk warning.
- `docker compose config --quiet` and `docker compose build gateway` pass.
- Redis sliding-window boundary, quota, and concurrency tests pass against the configured local Redis service.
- PostgreSQL integration passes against a fresh disposable PostgreSQL 17 database, including all five migrations, first-admin setup, login/logout, role checks, protected metrics, and last-admin safeguards.
- PostgreSQL replica synchronization integration passes for backend add/maintenance, route create/policy/disable, and load-balancing strategy/weight updates. `make check` passes after these changes.
- Burst allowance checks pass against Redis; fresh PostgreSQL integration verifies API-key policy update and route policy persistence/replica propagation with burst values. Dashboard build, lint, and all 13 frontend tests pass.
- Retry transport tests pass for transient failures, unsafe-method suppression, and non-retry of upstream HTTP error responses.
- `docker compose config --quiet`, gateway Docker image build, and Caddy validation through the existing Caddy container pass. The application stack was not restarted; only the disposable test database container was started and removed.

## Latest acceptance and remaining work

- Inspector history now checkpoints to PostgreSQL every two seconds and restores its bounded ring on startup; restart retention and ordering pass PostgreSQL integration tests.
- OpenTelemetry OTLP export and parent-context propagation pass a collector integration test. Four Prometheus alert rules pass promtool validation. External alert delivery still requires an operator-configured receiver.
- The isolated Compose acceptance and k6 runs pass; measurements and limits are recorded in scripts/LOAD_TESTING.md.
- Verified key lifecycle, two-backend distribution, throttling, Inspector correlation, protected metrics, logout, and active backend failure/recovery through the isolated Compose ingress.
- Remaining product gaps include backend health history and a complete final acceptance review. The full original product vision is not yet complete.

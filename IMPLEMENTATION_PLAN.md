# GateKeeper implementation and verification

Scope: the existing API gateway, read-only demonstration upstream, monitoring dashboard,
and Docker stack. The student endpoint is sample data, not a persistent student management application.

## Implemented

- Retain the existing Redis atomic fixed-window limiter, round-robin routing, and per-upstream transport circuit breakers.
- Reject unsupported upstream URLs and embedded credentials; rewrite upstream Host and strip gateway API credentials before forwarding.
- Reject empty authentication configuration and invalid rate-limit tuning; round fractional windows up to whole Redis TTL seconds.
- Preserve response status after streaming flush and informational HTTP responses.
- Add upstream HTTP timeouts and graceful termination.
- Populate dashboard backends from actual metric labels; bound fetch duration and prevent overlapping refreshes.
- Pass rate-limit configuration through Compose and align the frontend container with Node 24.
- Add regression tests, CI with Redis, Make targets, and an executable smoke check.
- Correct documentation to describe the implemented fixed-window algorithm.

## Repeatable validation

1. `make check` (Redis must listen on localhost:6379 to exercise Redis integration tests).
2. `docker compose up -d --build --wait` after configuring `.env` from `.env.example`.
3. `GATEKEEPER_API_KEY=... ALLOW_LOCAL_TLS=1 make smoke` for local Caddy certificates.
   For a public domain, set `GATEKEEPER_URL=https://your-domain` and omit `ALLOW_LOCAL_TLS`.
4. Check dashboard tabs and use the playground to observe backend identity and 429 responses.

## Deployment requirements

Configure your own API key, Redis password, Grafana administrator password, domain,
and certificate email. Existing local secrets are not overwritten by the implementation.
Circuit breakers intentionally track transport failures, not upstream HTTP 5xx responses.
Redis failure intentionally returns 503. `/health` is process liveness, not dependency readiness.

## Verified in this workspace

- Go tests with race detection and live Redis: pass.
- Go vet: pass.
- Frontend production build, lint, and three service regression tests: pass.
- Application Docker images build; gateway, both upstreams, and dashboard restarted healthy.
- HTTPS smoke checks: dashboard, health, unauthorized access, authenticated students,
  rate-limit headers, metrics, Prometheus queries, and round-robin routing pass.
- Remaining configuration: local `GRAFANA_ADMIN_PASSWORD` is unset. Existing Grafana
  data and credentials were preserved. Frontend build reports a non-blocking bundle-size warning.

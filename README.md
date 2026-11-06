# GateKeeper

GateKeeper is an API Gateway and Traffic Management Platform written in Go. It provides API-key authentication, Redis-backed sliding-window rate limiting, selectable health-aware load balancing with independent per-backend circuit breakers, active backend health checks, bounded request inspection, Prometheus metrics, dashboard sessions and account roles, and automated TLS termination via Caddy. PostgreSQL stores management configuration and dashboard identities; Redis stores transient rate-limit state.

---

## Architecture Overview

```mermaid
flowchart TD
    Client["Client / Browser / Application"]

    subgraph Host ["Linux VPS / Host Machine (Ports 80 & 443)"]
        subgraph Edge ["Edge Reverse Proxy & Auto-TLS"]
            Caddy["Caddy Server (:80, :443)<br/>Automated Let's Encrypt TLS"]
        end

        subgraph DockerCompose ["Internal Docker Network (gatekeeper_default)"]
            Frontend["Frontend Dashboard (:80)<br/>React + TS + Vite SPA"]
            Gateway["GateKeeper Gateway (:8080)<br/>Internal Only"]
            Backend1["Backend Instance 1 (:8081)<br/>Internal Only"]
            Backend2["Backend Instance 2 (:8081)<br/>Internal Only"]
            Redis[("Redis 7 (:6379)<br/>Auth Required | Internal Only")]
            Postgres[("PostgreSQL 17 (:5432)<br/>Management Data | Internal Only")]
            Prometheus["Prometheus (:9090)<br/>Internal Only"]
            Grafana["Grafana (:3000)<br/>Internal Only"]
        end
    end

    Client -->|"HTTPS (443) / HTTP (80)"| Caddy
    Caddy -->|"1. Static Assets (/*)"| Frontend
    Caddy -->|"2. API Requests (/api/*)"| Gateway
    Caddy -->|"3. Public Health (/health)"| Gateway
    Caddy -->|"4. Scoped Telemetry (/prometheus/api/v1/query*)"| Prometheus
    Caddy -.->|"5. Optional Subdomain"| Grafana

    Gateway -->|"Verify X-API-Key"| Gateway
    Gateway -->|"Atomic Lua Rate Check"| Redis
    Gateway -->|"Management Schema"| Postgres
    Gateway -->|"Round-Robin & Circuit Breaker"| Backend1
    Gateway -->|"Round-Robin & Circuit Breaker"| Backend2

    Prometheus -.->|"Internal scrape /metrics (every 5s)"| Gateway
    Grafana -->|"Query PromQL via Provisioned Datasource"| Prometheus

    subgraph Volumes ["Persistent Named Volumes"]
        RedisVol[("redis-data")] --- Redis
        PostgresVol[("postgres-data")] --- Postgres
        PromVol[("prometheus-data")] --- Prometheus
        GrafVol[("grafana-data")] --- Grafana
        CaddyData[("caddy-data")] --- Caddy
        CaddyConfig[("caddy-config")] --- Caddy
    end
```

---

## Features

- **Automated HTTPS & Edge Routing**: Caddy edge reverse proxy with automatic Let's Encrypt TLS certificate provisioning, HTTP-to-HTTPS redirection, and modern HTTP/2 + HTTP/3 support.
- **API-Key Authentication**: Constant-time verification of consumer `X-API-Key` credentials on routed API traffic. Dashboard management endpoints require a named dashboard session.
- **API Key Management**: PostgreSQL-backed consumer applications and API keys, hashed secret storage, expiry, read/write permissions, dynamically editable per-key quotas, usage counts, and revocation through protected management endpoints.
- **Atomic Redis Rate Limiting**: Sliding-window rate limiting powered by a Redis Lua script and sorted sets. Redis server time coordinates gateway instances; rejected requests do not extend a consumer's active window.
- **Per-Key and Per-Route Burst Allowance**: Administrators can configure a bounded number of additional requests within each policy's sliding window. Rate-limit headers report the combined quota, while Redis remains the shared atomic enforcement point.
- **Health-Aware Load Balancing**: Select round robin (default), weighted round robin, least in-flight requests, or random distribution. Unhealthy and circuit-open backends are excluded when healthy alternatives exist. `BACKEND_WEIGHTS` configures initial weights in the same order as `BACKEND_URLS`; authorized administrators can persist and change strategy and weights from the dashboard.
- **Active Backend Health History**: The health manager persists state transitions and failure reasons for configured backends. Protected history API results are bounded to the latest 100 events per backend and visible from Upstream Nodes.
- **Persistent Upstream and Route Management**: Bootstrap upstreams from `BACKEND_URLS`, then manage backend membership, weights, maintenance mode, health paths, route prefixes, methods, and authentication policy through protected APIs and dashboard pages. Route changes publish atomically and take effect without restarting the gateway.
- **Independent Circuit Breakers**: Dedicated per-backend circuit breakers with isolated failure thresholds, cooldown intervals, and half-open trial probes. Automatically skips failing backends and fast-fails with `503 Service Unavailable` and `Retry-After` if all backends are down.
- **Configurable Upstream Timeouts**: Gateway enforces bounded upstream `ResponseHeaderTimeout` (`BACKEND_TIMEOUT`, default: 10s). Slow or deadlocked backends are terminated with `502 Bad Gateway` and counted toward circuit breaker failure thresholds rather than hanging client connections indefinitely.
- **Bounded Upstream Retries**: `UPSTREAM_MAX_RETRIES` (default `0`, maximum `5`) and `UPSTREAM_RETRY_BACKOFF` configure transport-error retries for replayable GET, HEAD, OPTIONS, PUT, and DELETE requests. POST and other non-idempotent methods are never retried; HTTP responses such as 5xx are returned without retry. Retries run inside each backend's circuit breaker and honor request cancellation and the upstream timeout.
- **Credential Privacy**: Derives a deterministic SHA-256 hash from incoming API keys. Raw keys are never stored in Redis, printed to logs, or exposed.
- **Fail-Safe Design**: Returns `HTTP 503 Service Unavailable` if Redis is unreachable—never silently bypassing rate limits.
- **Interactive Monitoring Dashboard**: React + TypeScript + Vite single-page application served via a multi-stage Caddy container, featuring real-time telemetry, backend circuit breaker states, and an interactive API playground.
- **Scoped Telemetry Ingress**: Prometheus query access through the edge proxy is strictly restricted to query endpoints (`/prometheus/api/v1/query` and `/query_range`) used by the dashboard. Public `/metrics` and administrative Prometheus endpoints are blocked with `403 Forbidden`; Prometheus scrapes the gateway internally.
- **Restricted Cross-Origin Access**: Same-origin access is the default. Set `CORS_ALLOWED_ORIGINS` to explicit origins when hosting the dashboard separately; wildcard access is an explicit opt-in.
- **Internal Service Isolation**: Upstream backends, Redis, Prometheus, Grafana, and the gateway do not publish ports to the public host interface. All public traffic must enter through Caddy on ports 80 and 443.
- **Dashboard Access Control**: First-run setup uses the bootstrap `GATEKEEPER_API_KEY` once to create an administrator. Thereafter, management APIs require opaque PostgreSQL-backed session cookies; user passwords use bcrypt and stored session tokens are hashed. Admin, developer, and viewer roles restrict management access, and audit events record the named account.
- **PostgreSQL Management Layer**: Versioned startup migrations create dashboard accounts and sessions, applications, API keys, backends, routes, rate-limit policies, audit events, and settings. Consumer secrets are stored as SHA-256 hashes and returned once. Per-key and per-route limits and permissions are enforced by the gateway; Redis holds the sliding-window event state.

---

## Technology Stack

| Layer | Component | Description |
|---|---|---|
| **Edge Proxy** | Caddy 2.8 (`caddy:2.8-alpine`) | Automated TLS, reverse proxy, compression, and static asset serving |
| **API Gateway** | Go 1.26 | Standard library `net/http`, reverse proxy, load balancer, circuit breakers |
| **Dashboard** | React 19 + TypeScript + Vite + Tailwind | Modern real-time gateway developer dashboard |
| **Cache / Token Store** | Redis 7 (`redis:7-alpine`) | In-memory token counter with AOF persistence and password auth |
| **Management Database** | PostgreSQL 17 (`postgres:17-alpine`) | Persistent management schema and migration history; no rate-limit counters |
| **Metrics Collection** | Prometheus (`prom/prometheus:latest`) | Internal gateway scrape with scoped dashboard queries |
| **Visualization** | Grafana (`grafana/grafana:latest`) | Analytics dashboard provisioned with Prometheus data source |
| **Containerization** | Docker & Docker Compose v2 | Multi-container orchestrated deployment with logging limits |

---

## Production Deployment Guide (Single Linux VPS)

This guide walks through deploying GateKeeper to a single Linux Virtual Private Server (Ubuntu 22.04 LTS or Debian 12 recommended).

### 1. Prerequisites

1. **Linux VPS**: A cloud VPS (e.g. Hetzner, DigitalOcean, Linode, AWS EC2) with at least 1 GB RAM and 1 vCPU.
2. **DNS Records**: Configure your domain's DNS `A` (and optional `AAAA`) records to point to your VPS public IP:
   * `gatekeeper.yourdomain.com` $\rightarrow$ `<VPS-PUBLIC-IP>`
   * `grafana.yourdomain.com` (optional) $\rightarrow$ `<VPS-PUBLIC-IP>`
3. **Docker Engine & Compose**:
   ```bash
   # Install Docker using the official convenience script
   curl -fsSL https://get.docker.com | sh
   sudo usermod -aG docker $USER
   # Log out and log back in to apply group changes
   ```

### 2. Firewall Configuration (UFW)

Ensure only SSH (22), HTTP (80), and HTTPS (443) are publicly reachable. Internal services (`8080`, `8081`, `6379`, `9090`, `3000`) must be blocked from WAN:

```bash
# Enable UFW firewall
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw allow 22/tcp comment 'SSH'
sudo ufw allow 80/tcp comment 'Caddy HTTP'
sudo ufw allow 443/tcp comment 'Caddy HTTPS'
sudo ufw enable
sudo ufw status verbose
```

### 3. Clone Repository & Setup Environment

```bash
# Clone the repository
git clone https://github.com/pragyan43jain/GateKeeper.git
cd GateKeeper

# Copy the production environment template
cp .env.example .env
```

Generate cryptographically secure secrets and fill in `.env`:

For the dashboard, either set `DASHBOARD_ADMIN_EMAIL` and a 12–72 byte
`DASHBOARD_ADMIN_PASSWORD` before the first startup, or leave both blank and use
the first-run setup screen with `GATEKEEPER_API_KEY`. Once an administrator exists,
the bootstrap key no longer authenticates management APIs. Keep
`DASHBOARD_SESSION_COOKIE_SECURE=true` when serving the dashboard over HTTPS.

```bash
# Generate secrets
API_KEY=$(openssl rand -hex 24)
REDIS_PW=$(openssl rand -hex 24)
POSTGRES_PW=$(openssl rand -hex 24)
GRAFANA_PW=$(openssl rand -hex 24)

# Populate .env
sed -i "s/your-secure-gateway-api-key-here/$API_KEY/" .env
sed -i "s/your-secure-redis-password-here/$REDIS_PW/" .env
sed -i "s/your-secure-postgres-password-here/$POSTGRES_PW/" .env
sed -i "s/your-secure-grafana-password-here/$GRAFANA_PW/" .env
sed -i "s/DOMAIN_NAME=localhost/DOMAIN_NAME=gatekeeper.yourdomain.com/" .env
sed -i "s/ACME_EMAIL=admin@example.com/ACME_EMAIL=your-email@yourdomain.com/" .env
sed -i "s/GRAFANA_DOMAIN=grafana.localhost/GRAFANA_DOMAIN=grafana.yourdomain.com/" .env
```

Verify your `.env` contains valid values (do not share or commit this file):
```bash
grep -E '^(DOMAIN_NAME|ACME_EMAIL|GATEKEEPER_API_KEY|REDIS_PASSWORD|GRAFANA_ADMIN_PASSWORD)=' .env
```

### 4. Build and Start Services

```bash
# Build multi-stage images and start in detached mode
docker compose up -d --build
```

### 5. Verification & Health Check

1. **Verify Container Health**:
   ```bash
   docker compose ps
   ```
   All 9 containers (`caddy`, `frontend`, `gateway`, `backend-1`, `backend-2`, `redis`, `postgres`, `prometheus`, `grafana`) should show status `Up (healthy)`.

2. **Verify Public Health Endpoint (over HTTPS)**:
   ```bash
   curl -i https://gatekeeper.yourdomain.com/health
   # Expected: HTTP/2 200 OK with body "Gatekeeper is healthy"
   ```

3. **Verify Authenticated API Route & Load Balancing**:
   ```bash
   # Read your API key from .env
   KEY=$(grep GATEKEEPER_API_KEY .env | cut -d '=' -f2)

   # Send requests to observe round-robin alternation across backends
   curl -i -H "X-API-Key: $KEY" https://gatekeeper.yourdomain.com/api/students
   curl -i -H "X-API-Key: $KEY" https://gatekeeper.yourdomain.com/api/students
   # Check response headers: X-Backend-Id should alternate between backend-1 and backend-2
   ```

4. **Verify Unauthorized Requests**:
   ```bash
   curl -i https://gatekeeper.yourdomain.com/api/students
   # Expected: HTTP/2 401 Unauthorized
   ```

5. **Verify Rate Limiting**:
   Send rapid requests with your API key to observe the sliding-window limit. A policy with a burst of zero rejects after its configured request count; configured burst requests extend that count for the same window:
   ```bash
   for i in {1..11}; do
     curl -s -o /dev/null -w "%{http_code}\n" -H "X-API-Key: $KEY" https://gatekeeper.yourdomain.com/api/students
   done
   # Expected with the default zero burst: first 10 return 200, 11th returns 429 with Retry-After
   ```

6. **Verify Scoped Prometheus Protection & Method Restrictions**:
   ```bash
   # Permitted GET queries (returns HTTP/2 200 OK)
   curl -i https://gatekeeper.yourdomain.com/prometheus/api/v1/query?query=up
   curl -i "https://gatekeeper.yourdomain.com/prometheus/api/v1/query_range?query=up&start=$(($(date +%s)-60))&end=$(date +%s)&step=15s"

   # Disallowed HTTP method on query route (returns HTTP/2 405 Method Not Allowed)
   curl -i -X POST https://gatekeeper.yourdomain.com/prometheus/api/v1/query
   curl -i -X DELETE https://gatekeeper.yourdomain.com/prometheus/api/v1/query

   # Blocked administrative/diagnostic endpoints (returns HTTP/2 403 Forbidden)
   curl -i https://gatekeeper.yourdomain.com/prometheus/api/v1/admin/tsdb/clean_tombstones
   curl -i -X POST https://gatekeeper.yourdomain.com/prometheus/-/quit
   ```

7. **Verify Browser Security Headers**:
   ```bash
   curl -I https://gatekeeper.yourdomain.com/
   # Expected headers:
   # Strict-Transport-Security: max-age=31536000
   # X-Content-Type-Options: nosniff
   # X-Frame-Options: DENY
   # Referrer-Policy: strict-origin-when-cross-origin
   # Permissions-Policy: camera=(), microphone=(), geolocation=()
   # Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; ...
   ```

---

## Security Architecture & Hardening

### 1. Ingress & Browser Security Headers
The public Caddy reverse proxy terminates TLS and injects standardized browser defense-in-depth headers on all external responses:
- `Strict-Transport-Security`: Enforces HTTPS for all client sessions.
- `X-Content-Type-Options: nosniff`: Prevents MIME-type confusion attacks.
- `X-Frame-Options: DENY`: Prevents clickjacking and framing.
- `Referrer-Policy: strict-origin-when-cross-origin`: Minimizes referrer leakage.
- `Permissions-Policy`: Restricts browser sensors (camera, microphone, geolocation).
- `Content-Security-Policy`: Scoped to safely permit React + Vite scripts, dynamic SVG and styling used by Recharts (`'unsafe-inline'`), and outgoing API requests restricted strictly to same-origin (`connect-src 'self'`) while blocking rogue script/plugin execution.

### 2. Scoped Telemetry Ingress
- Only HTTP `GET` requests to `/prometheus/api/v1/query` and `/prometheus/api/v1/query_range` are forwarded to Prometheus for dashboard charts.
- Non-GET methods (POST, PUT, DELETE) on query endpoints are rejected at the edge with `HTTP 405 Method Not Allowed`.
- All other paths (`/graph`, `/api/v1/admin/*`, `/-/quit`, `/-/reload`) are rejected with `HTTP 403 Forbidden` (`Access Denied`).

### 3. Grafana Production Requirements
- **Credentials**:
  - `GRAFANA_ADMIN_USER`: Defaults to `admin`.
  - `GRAFANA_ADMIN_PASSWORD`: **Mandatory** in production `.env`. Must be set to a cryptographically random string (e.g. `openssl rand -hex 24`). Default fallbacks have been removed from Compose. If unset, Grafana starts with a blank password and standard logins are refused.
  - Public registration is permanently disabled (`GF_USERS_ALLOW_SIGN_UP=false`).
- **DNS Requirements**:
  - `GRAFANA_DOMAIN` (e.g. `grafana.yourdomain.com`) requires its own dedicated DNS `A` record pointing to the VPS public IP.
  - If the DNS record is missing, Caddy's ACME certificate challenge for the Grafana subdomain will fail.
- **Access Control**:
  - Direct host port 3000 is not exposed to the WAN.
  - If Grafana should not be public at all, operators can omit `GRAFANA_DOMAIN` from public DNS and access Grafana securely via an SSH tunnel:
    ```bash
    ssh -L 3000:localhost:3000 user@vps-ip
    ```

---

## Maintenance, Logs & Rollback

### Viewing Logs
```bash
# Tail all container logs
docker compose logs -f

# Tail Caddy access & TLS logs
docker compose logs -f caddy

# Tail Gateway proxy & circuit breaker logs
docker compose logs -f gateway
```

### Applying Configuration Updates
```bash
# Restart Caddy after Caddyfile edits
docker compose restart caddy

# Rebuild and restart after code changes
git pull origin main
docker compose up -d --build
```

### Rollback Procedure
If a deployment exhibits regressions:
```bash
# 1. Stop the current deployment
docker compose down

# 2. Checkout the known stable commit or tag
git checkout <stable-commit-hash>

# 3. Rebuild and restart
docker compose up -d --build
```

### Stopping Services & Data Preservation
```bash
# Stop containers (preserves all volume data)
docker compose down

# Warning: the following command destroys rate-limit data, management data, and Prometheus metrics:
# docker compose down -v
```

---

## Local Development Workflows

### Load Balancing Configuration

Set `LOAD_BALANCE_STRATEGY` to `round_robin`, `weighted_round_robin`, `least_connections`, or `random`.
For weighted round robin, set one integer in the range 1–1000 per configured backend, in URL order;
for example, `BACKEND_WEIGHTS=2,1` gives the first backend twice the weight of the second.
The `least_connections` strategy chooses the backend with the fewest in-flight proxied requests.
These environment variables provide the initial settings; persisted dashboard changes override them on subsequent gateway starts. The default strategy remains round robin.

### Workflow A: Local Ingress Development (Recommended)
You can run the full containerized stack locally without touching raw host ports:
```bash
# 1. Start the stack (Caddy listens on localhost:80 and localhost:443)
docker compose up -d --build

# 2. Start the Vite dev server for hot-reload frontend development
cd frontend
npm install
npm run dev
# The Vite dev proxy automatically routes /api, /health, /metrics, and /prometheus
# to https://localhost (Caddy) without needing raw gateway/prometheus ports.
```

### Workflow B: Direct Loopback Debugging (`docker-compose.dev.yml`)
If you require direct access to internal container ports for debugging, use the development override which binds ports strictly to `127.0.0.1` (never `0.0.0.0`):
```bash
# Start with the development override
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d

# Direct loopback endpoints available:
# - Gateway:    http://127.0.0.1:8082
# - Prometheus: http://127.0.0.1:9090
# - Grafana:    http://127.0.0.1:3000

# Run frontend against raw gateway port:
cd frontend
VITE_PROXY_TARGET=http://localhost:8082 npm run dev
```

### Running Backend Tests
```bash
go test -v -count=1 ./...
go test -race -v -count=1 ./...
go vet ./...
```


## Project checks

Inspector history is bounded by `MAX_LOG_ENTRIES` (default 500). With PostgreSQL
enabled, each gateway checkpoints its ring every two seconds and on graceful
shutdown, then restores it at startup. An abrupt crash can lose records since
the last successful checkpoint; this is diagnostic history, not an audit ledger.
Set a stable, unique `GATEWAY_INSTANCE_ID` for each replica. History queries return
the current replica's records. Remove snapshots for retired instance IDs as part
of decommissioning. Database checkpoint failures are logged while traffic continues.

Load-test instructions and alert-delivery limitations are in
[scripts/LOAD_TESTING.md](scripts/LOAD_TESTING.md).

Gateway tracing exports to an OTLP HTTP collector when `OTEL_EXPORTER_OTLP_ENDPOINT`
is configured. W3C trace context is preserved and a gateway span is forwarded to
upstreams. Root requests use 10% sampling; sampled parent traces are retained.
Only request method and response status are attached as HTTP attributes. Shutdown
flushes pending spans within five seconds. A collector is not provisioned by the
default Compose stack; leaving the endpoint empty disables export.

The current implementation scope and validation steps are in [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).
Run `make check` for Go race tests, vet, frontend build, and lint. Redis integration tests
require Redis on `localhost:6379`; CI provisions it automatically. The PostgreSQL migration
integration test runs when `TEST_DATABASE_URL` points to a disposable PostgreSQL database.
Run `make up` to build and start the stack, then use `make smoke` with
`GATEKEEPER_API_KEY` set. For local self-signed Caddy TLS only, set `ALLOW_LOCAL_TLS=1`.
The backend is a read-only demonstration service; student records are sample data.

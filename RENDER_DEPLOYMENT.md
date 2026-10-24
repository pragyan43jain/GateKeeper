# GateKeeper Render Backend Deployment Guide

This guide details how to deploy the GateKeeper backend infrastructure (Gateway, Upstream Backends, and Redis Key-Value Store) to Render's Free Tier to integrate with your deployed Vercel frontend.

---

## 1. Free-Tier Architecture Overview

```
                                +---------------------------------------------------+
                                |                  Vercel (Hobby)                   |
                                |  https://frontend-zeta-pearl-bt8xd0e8u3.vercel.app|
                                +-------------------------+-------------------------+
                                                          |
                                      HTTPS Requests (with X-API-Key)
                                      + CORS (Allowed Origin)
                                                          v
                                +---------------------------------------------------+
                                |            Render Free Web Service                |
                                |               gatekeeper-gateway                  |
                                |       https://<gateway-slug>.onrender.com         |
                                +------------+----------------------+---------------+
                                             |                      |
                    HTTPS Round-Robin        |                      | In-memory Lua
                   + Circuit Breaker         |                      | Rate Limiting
                         +-------------------+                      |
                         |                   |                      v
                         v                   v            +-------------------+
               +-------------------+ +-------------------+| Render Key Value / |
               |gatekeeper-backend1| |gatekeeper-backend2||  Valkey (Free)    |
               | (Free Web Service)| | (Free Web Service)|| (Private Network) |
               +-------------------+ +-------------------++-------------------+
```

### Architecture Rationale:
1. **Frontend (Vercel)**: Deployed as a single-page React application at `https://frontend-zeta-pearl-bt8xd0e8u3.vercel.app`.
2. **Gateway (Render Free Web Service)**: Listens on `$PORT` (default 8080), verifies `X-API-Key`, applies atomic Lua sliding-window rate limiting via Redis, performs round-robin load balancing across backends with isolated circuit breakers, exposes public `/health` and `/metrics`, and handles browser CORS preflights.
3. **Upstream Backends (`backend-1` and `backend-2`)**: Render free tier does not support receiving inbound traffic over private networking. Therefore, both backends are deployed as free Web Services exposing their standard `/api/students` endpoint over HTTPS.
4. **Redis Store (`gatekeeper-redis`)**: Render free Key Value instance (running Redis-compatible Valkey) providing in-memory key storage for the 60-second sliding-window rate limiter without time-based expiration. (Alternative: Upstash Redis free tier).
5. **Telemetry & Monitoring**: Prometheus and Grafana admin interfaces are **not** deployed publicly to avoid exposing management interfaces, avoid exceeding the 512 MB memory limit, and conserve free instance hours. The gateway's native `/metrics` endpoint provides Prometheus-standard exposition format.

---

## 2. Render Free-Tier Limitations & Important Notes

| Characteristic | Render Free Tier Behavior | Impact on GateKeeper |
|---|---|---|
| **Inactivity Spin-Down** | Web services automatically spin down after 15 minutes of zero traffic. | First request after idle takes 30–50 seconds (cold start). `BACKEND_TIMEOUT` is set to `60s` to prevent premature circuit breaker trips during backend wake-ups. |
| **Instance Hours Pool** | 750 free instance hours per calendar month shared across all free services. | Running 3 free web services continuously would consume 3 hours per hour (~10 days total). Inactivity spin-down helps preserve hours, but for continuous 24/7 uptime, a single-service architecture or paid plan ($7/mo per service) is required. |
| **Private Networking** | Free web services **cannot receive** inbound private traffic. | Backends must be deployed as Web Services reachable via their public HTTPS URLs. Outbound traffic to the free Key Value store over the private network is fully supported. |
| **Key Value Persistence** | Render Key Value free tier has no disk persistence (max 25 MB RAM). | Restarts flush in-memory keys. For 60-second rate-limiting tokens, this is completely acceptable. If persistent counters across restarts are required, use Upstash Redis (serverless free tier). |

---

## 3. Deployment Methods

### Option A: Automated Deployment via Render Blueprint (`render.yaml`)

1. Push your repository to GitHub:
   ```bash
   git push origin main
   ```
2. Log in to your [Render Dashboard](https://dashboard.render.com/).
3. Click **New +** > **Blueprint**.
4. Connect your `GateKeeper` repository.
5. Render will detect `render.yaml` and prompt to create:
   - `gatekeeper-redis` (Key Value, Free)
   - `gatekeeper-backend-1` (Web Service, Free)
   - `gatekeeper-backend-2` (Web Service, Free)
   - `gatekeeper-gateway` (Web Service, Free)
6. Click **Apply**.
7. Once `gatekeeper-backend-1` and `gatekeeper-backend-2` are live, copy their public URLs (e.g. `https://gatekeeper-backend-1.onrender.com` and `https://gatekeeper-backend-2.onrender.com`).
8. Go to `gatekeeper-gateway` > **Environment** and set:
   ```env
   BACKEND_URLS=https://gatekeeper-backend-1.onrender.com,https://gatekeeper-backend-2.onrender.com
   ```
9. Save and redeploy the gateway.

---

### Option B: Manual Dashboard Setup

#### Step 1: Create Redis Key Value Store
1. Click **New +** > **Key Value**.
2. Name: `gatekeeper-redis`.
3. Plan: **Free** (25 MB).
4. Click **Create Key Value**.
5. Copy the **Internal Connection String** (e.g., `redis://red-xxxx:6379`).

#### Step 2: Create Upstream Backends
Repeat for `gatekeeper-backend-1` and `gatekeeper-backend-2`:
1. Click **New +** > **Web Service**.
2. Connect your Git repository.
3. Name: `gatekeeper-backend-1` (and `gatekeeper-backend-2`).
4. Runtime: **Docker**.
5. Dockerfile Path: `./backend/Dockerfile`.
6. Plan: **Free**.
7. Health Check Path: `/api/students`.
8. Under **Environment Variables**, add:
   - `BACKEND_ID` = `backend-1` (or `backend-2`)
9. Click **Create Web Service**.

#### Step 3: Create Gateway
1. Click **New +** > **Web Service**.
2. Connect your Git repository.
3. Name: `gatekeeper-gateway`.
4. Runtime: **Docker**.
5. Dockerfile Path: `./gateway/Dockerfile`.
6. Plan: **Free**.
7. Health Check Path: `/health`.
8. Under **Environment Variables**, configure:

| Variable Name | Example Value / Purpose |
|---|---|
| `PORT` | `8080` |
| `GATEKEEPER_API_KEY` | *(Secret secure string, e.g. generate via `openssl rand -hex 24`)* |
| `REDIS_URL` | *(Paste Internal Connection String from Step 1, or Upstash Redis URL)* |
| `BACKEND_URLS` | `https://gatekeeper-backend-1.onrender.com,https://gatekeeper-backend-2.onrender.com` |
| `CORS_ALLOWED_ORIGINS` | `https://frontend-8zy9berte-pragyan43jains-projects.vercel.app,https://frontend-zeta-pearl-bt8xd0e8u3.vercel.app` |
| `BACKEND_TIMEOUT` | `60s` *(Allows upstream cold starts without tripping circuit breakers)* |
| `CIRCUIT_BREAKER_FAILURE_THRESHOLD`| `5` |
| `CIRCUIT_BREAKER_COOLDOWN` | `10s` |
| `RATE_LIMIT_MAX_REQUESTS` | `10` |
| `RATE_LIMIT_WINDOW` | `60s` |

9. Click **Create Web Service**.

---

## 4. Connecting the Frontend to the Gateway

Once `gatekeeper-gateway` is deployed:
1. Copy its production URL (e.g., `https://gatekeeper-gateway.onrender.com`).
2. Add the environment variable in Vercel:
   ```bash
   vercel env add VITE_API_URL production
   # Enter value: https://gatekeeper-gateway.onrender.com
   ```
3. Redeploy the frontend:
   ```bash
   vercel deploy --prod --yes
   ```
4. Access `https://frontend-zeta-pearl-bt8xd0e8u3.vercel.app`:
   - System health will report **Healthy**.
   - Live metrics from the gateway will stream into the dashboard.
   - Enter your `GATEKEEPER_API_KEY` in the API Playground to test round-robin load balancing and rate limiting.

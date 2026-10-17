# syntax=docker/dockerfile:1

# Stage 1: Build production static assets
FROM node:20-alpine AS builder

WORKDIR /app

# Cache package installation
COPY package.json package-lock.json ./
RUN npm ci

# Copy source code and build
COPY . .
RUN npm run build

# Stage 2: Minimal runtime image serving static assets via Caddy
FROM caddy:2.8-alpine

WORKDIR /srv

# Copy compiled static assets from builder stage
COPY --from=builder /app/dist /srv

# Copy frontend Caddyfile for static file serving and SPA fallback
COPY Caddyfile /etc/caddy/Caddyfile

EXPOSE 80

CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile"]

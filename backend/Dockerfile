# syntax=docker/dockerfile:1

# Stage 1: Build the backend binary
FROM golang:alpine AS builder

WORKDIR /app

# Leverage caching for dependency downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /backend ./backend

# Stage 2: Minimal runtime image
FROM alpine:latest

RUN apk --no-cache add ca-certificates tzdata wget && \
    addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

COPY --from=builder /backend /backend

USER appuser

EXPOSE 8081

ENTRYPOINT ["/backend"]


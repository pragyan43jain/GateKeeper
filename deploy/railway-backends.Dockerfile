FROM golang:alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY backend ./backend
RUN CGO_ENABLED=0 go build -o /backend ./backend
FROM alpine:latest
RUN apk add --no-cache ca-certificates bash && adduser -D app
COPY --from=builder /backend /backend
COPY deploy/railway-backends.sh /start-backends
USER app
ENTRYPOINT ["/bin/bash", "/start-backends"]

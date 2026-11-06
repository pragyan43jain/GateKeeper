package main

import (
	"io"
	"log"
	"log/slog"
)

// configureJSONLogging emits structured JSON through both slog and the legacy
// standard logger so container logs have one machine-readable format.
func configureJSONLogging(output io.Writer) {
	handler := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})
	slog.SetDefault(slog.New(handler))
	log.SetFlags(0)
	log.SetOutput(slog.NewLogLogger(handler, slog.LevelInfo).Writer())
}

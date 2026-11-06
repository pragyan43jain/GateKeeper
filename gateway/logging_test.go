package main

import (
	"bytes"
	"encoding/json"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestConfigureJSONLoggingFormatsSlogAndStandardLog(t *testing.T) {
	previousSlog := slog.Default()
	previousOutput := log.Writer()
	previousFlags := log.Flags()
	defer func() {
		slog.SetDefault(previousSlog)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	}()

	var output bytes.Buffer
	configureJSONLogging(&output)
	slog.Info("gateway request", "request_id", "req-test")
	log.Printf("legacy failure: %s", "upstream timeout")

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two JSON log records, got %d: %q", len(lines), output.String())
	}
	var structured, legacy map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &structured); err != nil {
		t.Fatalf("decode slog record: %v", err)
	}
	if structured["msg"] != "gateway request" || structured["request_id"] != "req-test" {
		t.Fatalf("unexpected structured log: %+v", structured)
	}
	if err := json.Unmarshal([]byte(lines[1]), &legacy); err != nil {
		t.Fatalf("decode standard log record: %v", err)
	}
	if legacy["msg"] != "legacy failure: upstream timeout" || legacy["level"] != "INFO" {
		t.Fatalf("unexpected standard log record: %+v", legacy)
	}
}

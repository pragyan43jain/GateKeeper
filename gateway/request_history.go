package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"time"
)

// startRequestHistory restores the bounded local ring and checkpoints it without
// putting database writes on the request path. Each replica needs a stable ID.
func startRequestHistory(store *ManagementStore, logger *RequestLogger, instance string) (func(), error) {
	if store == nil {
		return func() {}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var data []byte
	err := store.db.QueryRowContext(ctx, `SELECT entries FROM request_history_snapshots WHERE instance_id = $1`, instance).Scan(&data)
	cancel()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if len(data) != 0 {
		var entries []RequestLogEntry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, err
		}
		// Snapshots are newest first; restore oldest first to preserve ring ordering.
		if len(entries) > logger.capacity {
			entries = entries[:logger.capacity]
		}
		for i := len(entries) - 1; i >= 0; i-- {
			logger.Record(entries[i])
		}
	}
	save := func() {
		entries := logger.GetEntries(logger.capacity, 0, "", "")
		data, err := json.Marshal(entries)
		if err != nil {
			log.Printf("request_history_encode_failed")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, err = store.db.ExecContext(ctx, `INSERT INTO request_history_snapshots(instance_id, entries) VALUES ($1, $2)
   ON CONFLICT (instance_id) DO UPDATE SET entries = EXCLUDED.entries, updated_at = now()`, instance, data)
		if err != nil {
			log.Printf("request_history_checkpoint_failed")
		}
	}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				save()
			case <-stop:
				save()
				return
			}
		}
	}()
	return func() { close(stop); <-done }, nil
}

// ListRequestHistoryEntries merges the bounded snapshots written by all gateway
// instances. IDs de-duplicate a local in-memory record against its last snapshot.
func (s *ManagementStore) ListRequestHistoryEntries(ctx context.Context) ([]RequestLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT entries FROM request_history_snapshots ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]RequestLogEntry, 0)
	seen := make(map[string]struct{})
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var snapshot []RequestLogEntry
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, err
		}
		for _, entry := range snapshot {
			if entry.ID == "" {
				continue
			}
			if _, exists := seen[entry.ID]; exists {
				continue
			}
			seen[entry.ID] = struct{}{}
			entries = append(entries, entry)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, entries[i].Timestamp)
		right, rightErr := time.Parse(time.RFC3339Nano, entries[j].Timestamp)
		if leftErr != nil || rightErr != nil {
			return entries[i].Timestamp > entries[j].Timestamp
		}
		return left.After(right)
	})
	return entries, nil
}

func mergeRequestLogEntries(preferred, fallback []RequestLogEntry) []RequestLogEntry {
	entries := make([]RequestLogEntry, 0, len(preferred)+len(fallback))
	seen := make(map[string]struct{}, len(preferred)+len(fallback))
	for _, group := range [][]RequestLogEntry{preferred, fallback} {
		for _, entry := range group {
			if entry.ID == "" {
				continue
			}
			if _, exists := seen[entry.ID]; exists {
				continue
			}
			seen[entry.ID] = struct{}{}
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, leftErr := time.Parse(time.RFC3339Nano, entries[i].Timestamp)
		right, rightErr := time.Parse(time.RFC3339Nano, entries[j].Timestamp)
		if leftErr != nil || rightErr != nil {
			return entries[i].Timestamp > entries[j].Timestamp
		}
		return left.After(right)
	})
	return entries
}

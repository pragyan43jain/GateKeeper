CREATE TABLE request_history_snapshots (
    instance_id TEXT PRIMARY KEY,
    entries JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

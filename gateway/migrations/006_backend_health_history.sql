CREATE TABLE IF NOT EXISTS backend_health_history (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    backend_id UUID NOT NULL REFERENCES backends(id) ON DELETE CASCADE,
    backend_name TEXT NOT NULL,
    previous_state TEXT NOT NULL CHECK (previous_state IN ('healthy', 'unhealthy', 'recovering')),
    state TEXT NOT NULL CHECK (state IN ('healthy', 'unhealthy', 'recovering')),
    reason TEXT NOT NULL DEFAULT '',
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS backend_health_history_lookup_idx
    ON backend_health_history(backend_id, checked_at DESC, id DESC);

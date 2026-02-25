CREATE TABLE IF NOT EXISTS telemetry (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  prefix_length INTEGER NOT NULL,
  duration_seconds REAL NOT NULL,
  cores_used INTEGER NOT NULL,
  attempts INTEGER NOT NULL,
  submitted_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_prefix_length ON telemetry(prefix_length);
CREATE INDEX IF NOT EXISTS idx_submitted_at ON telemetry(submitted_at);

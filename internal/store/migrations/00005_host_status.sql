-- +goose Up
CREATE TABLE host_status (
  host       TEXT NOT NULL,
  scope      TEXT NOT NULL,          -- "<project>#<env>"
  reachable  INTEGER NOT NULL,
  latency_ms INTEGER NOT NULL DEFAULT 0,
  msg        TEXT NOT NULL DEFAULT '',
  checked_at TIMESTAMP NOT NULL,
  PRIMARY KEY (host, scope)
);

-- +goose Down
DROP TABLE host_status;

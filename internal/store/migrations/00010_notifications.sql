-- +goose Up
-- Run notifications: what finished, failed, waits for approval or drifted.
-- One row per (run, event); kept 30 days.
CREATE TABLE notifications (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    event       TEXT NOT NULL,
    run_id      INTEGER NOT NULL,
    kind        TEXT NOT NULL DEFAULT '',
    stage       TEXT NOT NULL DEFAULT '',
    label       TEXT NOT NULL DEFAULT '',
    root        TEXT NOT NULL DEFAULT '',
    env         TEXT NOT NULL DEFAULT '',
    title       TEXT NOT NULL,
    text        TEXT NOT NULL DEFAULT '',
    cause_json  TEXT NOT NULL DEFAULT '',
    plan_json   TEXT NOT NULL DEFAULT '',
    drift_count INTEGER NOT NULL DEFAULT 0,
    duration_s  INTEGER NOT NULL DEFAULT 0,
    at          TIMESTAMP NOT NULL,
    read_at     TIMESTAMP,
    toast       INTEGER NOT NULL DEFAULT 0,
    desktop     INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX notifications_run_event ON notifications(run_id, event);
CREATE INDEX notifications_at ON notifications(at);

-- +goose Down
DROP TABLE notifications;

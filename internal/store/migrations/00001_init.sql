-- +goose Up
CREATE TABLE runs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  kind         TEXT NOT NULL,
  target_json  TEXT NOT NULL DEFAULT '{}',
  argv_json    TEXT NOT NULL DEFAULT '[]',
  status       TEXT NOT NULL,
  stage        TEXT NOT NULL DEFAULT '',
  "commit"     TEXT NOT NULL DEFAULT '',
  started_at   TIMESTAMP,
  ended_at     TIMESTAMP,
  exit_code    INTEGER,
  summary_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX runs_status ON runs(status);

CREATE TABLE run_stages (
  run_id     INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  status     TEXT NOT NULL,
  started_at TIMESTAMP,
  ended_at   TIMESTAMP,
  detail     TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (run_id, name)
);

CREATE TABLE approvals (
  run_id        INTEGER PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
  plan_sha256   TEXT NOT NULL,
  state_serial  INTEGER NOT NULL,
  approved_by   TEXT NOT NULL,
  approved_at   TIMESTAMP NOT NULL,
  confirm_text  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE diagnostics (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  unit         TEXT NOT NULL,
  tool         TEXT NOT NULL,
  severity     TEXT NOT NULL,
  code         TEXT NOT NULL DEFAULT '',
  file         TEXT NOT NULL DEFAULT '',
  line         INTEGER NOT NULL DEFAULT 0,
  col          INTEGER NOT NULL DEFAULT 0,
  message      TEXT NOT NULL,
  fix_json     TEXT,
  content_hash TEXT NOT NULL DEFAULT '',
  seen_at      TIMESTAMP NOT NULL
);
CREATE INDEX diagnostics_unit ON diagnostics(unit);

CREATE TABLE issues (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  rule_id      TEXT NOT NULL,
  target       TEXT NOT NULL DEFAULT '',
  status       TEXT NOT NULL,
  first_seen   TIMESTAMP NOT NULL,
  last_seen    TIMESTAMP NOT NULL,
  context_json TEXT NOT NULL DEFAULT '{}',
  resolved_at  TIMESTAMP
);

CREATE TABLE drift (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  root         TEXT NOT NULL,
  env          TEXT NOT NULL DEFAULT '',
  address      TEXT NOT NULL,
  attr_path    TEXT NOT NULL DEFAULT '',
  code_value   TEXT,
  actual_value TEXT,
  detected_at  TIMESTAMP NOT NULL
);

CREATE TABLE facts (
  host        TEXT NOT NULL,
  env         TEXT NOT NULL DEFAULT '',
  json        TEXT NOT NULL,
  gathered_at TIMESTAMP NOT NULL,
  PRIMARY KEY (host, env)
);

CREATE TABLE kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- +goose Down
DROP TABLE kv;
DROP TABLE facts;
DROP TABLE drift;
DROP TABLE issues;
DROP TABLE diagnostics;
DROP TABLE approvals;
DROP TABLE run_stages;
DROP TABLE runs;

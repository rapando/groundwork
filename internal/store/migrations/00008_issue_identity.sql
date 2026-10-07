-- +goose Up
ALTER TABLE issues ADD COLUMN key TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN source TEXT NOT NULL DEFAULT '';
ALTER TABLE issues ADD COLUMN resolution TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX issues_identity ON issues(rule_id, target, key);

-- +goose Down
DROP INDEX issues_identity;
ALTER TABLE issues DROP COLUMN resolution;
ALTER TABLE issues DROP COLUMN source;
ALTER TABLE issues DROP COLUMN key;

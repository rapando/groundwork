-- +goose Up
ALTER TABLE runs ADD COLUMN created_at TIMESTAMP;
UPDATE runs SET created_at = COALESCE(started_at, CURRENT_TIMESTAMP) WHERE created_at IS NULL;
CREATE INDEX runs_created ON runs(created_at);

-- +goose Down
DROP INDEX runs_created;
ALTER TABLE runs DROP COLUMN created_at;

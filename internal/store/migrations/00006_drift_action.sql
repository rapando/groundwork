-- +goose Up
ALTER TABLE drift ADD COLUMN action TEXT NOT NULL DEFAULT 'update';
ALTER TABLE drift ADD COLUMN run_id INTEGER NOT NULL DEFAULT 0;
CREATE INDEX drift_target ON drift(root, env);

-- +goose Down
DROP INDEX drift_target;
ALTER TABLE drift DROP COLUMN run_id;
ALTER TABLE drift DROP COLUMN action;

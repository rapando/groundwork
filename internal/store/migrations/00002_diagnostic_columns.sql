-- +goose Up
ALTER TABLE diagnostics ADD COLUMN detail   TEXT NOT NULL DEFAULT '';
ALTER TABLE diagnostics ADD COLUMN end_line INTEGER NOT NULL DEFAULT 0;
ALTER TABLE diagnostics ADD COLUMN end_col  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE diagnostics ADD COLUMN link     TEXT NOT NULL DEFAULT '';
CREATE INDEX diagnostics_file ON diagnostics(file);

-- +goose Down
DROP INDEX diagnostics_file;
ALTER TABLE diagnostics DROP COLUMN link;
ALTER TABLE diagnostics DROP COLUMN end_col;
ALTER TABLE diagnostics DROP COLUMN end_line;
ALTER TABLE diagnostics DROP COLUMN detail;

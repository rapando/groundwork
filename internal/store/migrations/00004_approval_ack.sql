-- +goose Up
ALTER TABLE approvals ADD COLUMN acknowledged_danger INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE approvals DROP COLUMN acknowledged_danger;

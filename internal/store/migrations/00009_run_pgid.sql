-- +goose Up
-- The process group of the tool a run started, so that after a hard stop
-- (SIGKILL, power loss) groundwork can tell whether terraform or ansible is
-- still running and say so.
ALTER TABLE runs ADD COLUMN pgid INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE runs DROP COLUMN pgid;

-- +goose Up
-- Audit of secret reveals: which secret and when. Never the value.
CREATE TABLE secret_reveals (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT NOT NULL,
    name        TEXT NOT NULL,
    file        TEXT NOT NULL DEFAULT '',
    revealed_at TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE secret_reveals;

-- +goose Up
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO settings (key, value) VALUES ('routing_policy', 'most_free');

-- +goose Down
DROP TABLE settings;

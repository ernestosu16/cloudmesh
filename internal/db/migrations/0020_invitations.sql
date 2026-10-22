-- +goose Up
-- Invitaciones: enlace de un solo uso para que alguien elija su contraseña.
-- Se guarda el SHA-256 del token, no el token. accepted_at > 0: ya usada.
CREATE TABLE invitations (
    id          INTEGER PRIMARY KEY,
    token_hash  TEXT    NOT NULL UNIQUE,
    email       TEXT    NOT NULL,
    role        TEXT    NOT NULL,
    quota_bytes INTEGER NOT NULL DEFAULT 0,
    created_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    accepted_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX invitations_email ON invitations(email);

-- +goose Down
DROP TABLE invitations;

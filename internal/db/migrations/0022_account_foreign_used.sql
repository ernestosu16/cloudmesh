-- +goose Up
-- Lo usado en la cuenta que no es de CloudMesh, al último sondeo. El uso propio
-- sale de los bloques; sumado a esto da lo que ocupa la cuenta real.
ALTER TABLE accounts ADD COLUMN foreign_used INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE accounts DROP COLUMN foreign_used;

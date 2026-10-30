-- A tenant is one of our systems. system_code links it to that system in ATM-Auth-API,
-- so the system's service token (subject "system:<code>") identifies the tenant and
-- the API key is no longer needed.

-- +goose Up
ALTER TABLE tenants ADD COLUMN system_code VARCHAR(50);
CREATE UNIQUE INDEX tenants_system_code_key ON tenants (system_code) WHERE system_code IS NOT NULL;

-- +goose Down
DROP INDEX tenants_system_code_key;
ALTER TABLE tenants DROP COLUMN system_code;

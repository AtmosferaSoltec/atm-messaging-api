-- A tenant is now created by messaging itself the first time a system presents its
-- service token, so it is identified only by system_code. API keys are gone.

-- +goose Up
DROP INDEX tenants_api_key_hash_key;
ALTER TABLE tenants DROP COLUMN api_key_prefix, DROP COLUMN api_key_hash;

DROP INDEX tenants_system_code_key;
ALTER TABLE tenants ALTER COLUMN system_code TYPE TEXT, ALTER COLUMN system_code SET NOT NULL;
ALTER TABLE tenants ADD CONSTRAINT tenants_system_code_valid CHECK (system_code ~ '^[a-z][a-z0-9_-]*$');
CREATE UNIQUE INDEX tenants_system_code_key ON tenants (system_code);

-- +goose Down
DROP INDEX tenants_system_code_key;
ALTER TABLE tenants DROP CONSTRAINT tenants_system_code_valid;
ALTER TABLE tenants ALTER COLUMN system_code DROP NOT NULL, ALTER COLUMN system_code TYPE VARCHAR(50);
CREATE UNIQUE INDEX tenants_system_code_key ON tenants (system_code) WHERE system_code IS NOT NULL;

ALTER TABLE tenants ADD COLUMN api_key_prefix VARCHAR(16) NOT NULL DEFAULT '', ADD COLUMN api_key_hash CHAR(64);
UPDATE tenants SET api_key_hash = encode(sha256(id::text::bytea), 'hex');
ALTER TABLE tenants ALTER COLUMN api_key_hash SET NOT NULL;
CREATE UNIQUE INDEX tenants_api_key_hash_key ON tenants (api_key_hash);

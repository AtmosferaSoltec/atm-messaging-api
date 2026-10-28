-- Video and audio messages, and unique tenant names.

-- +goose Up
ALTER TABLE messages DROP CONSTRAINT messages_type_valid;
ALTER TABLE messages ADD CONSTRAINT messages_type_valid
    CHECK (type IN ('text', 'image', 'video', 'audio', 'document'));

-- Names were free text, so duplicates may already exist (e.g. from testing). Keep
-- the oldest as it is and number the rest, so nothing is deleted and the index can be created.
WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY lower(btrim(name)) ORDER BY created_at, id) AS n
    FROM tenants
)
UPDATE tenants t SET name = left(t.name, 90) || ' (' || r.n || ')'
FROM ranked r WHERE r.id = t.id AND r.n > 1;

CREATE UNIQUE INDEX tenants_name_lower_key ON tenants (lower(btrim(name)));

-- +goose Down
DROP INDEX tenants_name_lower_key;
DELETE FROM messages WHERE type IN ('video', 'audio');
ALTER TABLE messages DROP CONSTRAINT messages_type_valid;
ALTER TABLE messages ADD CONSTRAINT messages_type_valid CHECK (type IN ('text', 'image', 'document'));

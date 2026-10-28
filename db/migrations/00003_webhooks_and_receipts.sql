-- Phase 4: outgoing webhooks (transactional outbox) and delivery receipts.

-- +goose Up
ALTER TABLE tenants ADD COLUMN webhook_url VARCHAR(2048);

ALTER TABLE messages
    ADD COLUMN delivered_at TIMESTAMPTZ,
    ADD COLUMN read_at      TIMESTAMPTZ;
-- Receipts identify a message by the provider's ID within a session.
CREATE INDEX messages_provider_id_idx ON messages (session_id, provider_message_id)
    WHERE provider_message_id <> '';

-- One row per webhook to deliver. The body is stored ready to send, so every
-- retry carries the same event ID and content.
CREATE TABLE webhook_events (
    id              UUID         PRIMARY KEY,
    tenant_id       UUID         NOT NULL REFERENCES tenants (id),
    event_type      VARCHAR(50)  NOT NULL,
    payload         JSONB        NOT NULL,
    status          VARCHAR(20)  NOT NULL DEFAULT 'pending',
    attempts        INT          NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_error      VARCHAR(500) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ,
    CONSTRAINT webhook_events_status_valid CHECK (status IN ('pending', 'delivered', 'failed'))
);
CREATE INDEX webhook_events_due_idx ON webhook_events (tenant_id, next_attempt_at) WHERE status = 'pending';
CREATE INDEX webhook_events_tenant_created_idx ON webhook_events (tenant_id, created_at DESC);

-- +goose Down
DROP TABLE webhook_events;
DROP INDEX messages_provider_id_idx;
ALTER TABLE messages DROP COLUMN read_at, DROP COLUMN delivered_at;
ALTER TABLE tenants DROP COLUMN webhook_url;

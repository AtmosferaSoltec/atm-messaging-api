-- Transactional email: a persistent queue. One row per message to send. The data
-- column holds the template variables; it is blanked for password reset codes once
-- the message is sent or has failed for good.

-- +goose Up
CREATE TABLE email_messages (
    id              UUID         PRIMARY KEY,
    tenant_id       UUID         NOT NULL REFERENCES tenants (id),
    to_address      VARCHAR(254) NOT NULL,
    template        VARCHAR(50)  NOT NULL,
    data            JSONB        NOT NULL DEFAULT '{}',
    idempotency_key VARCHAR(128),
    status          VARCHAR(20)  NOT NULL DEFAULT 'queued',
    attempts        INT          NOT NULL DEFAULT 0,
    last_error      VARCHAR(500) NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    CONSTRAINT email_messages_status_valid CHECK (status IN ('queued', 'sending', 'sent', 'failed'))
);
CREATE UNIQUE INDEX email_messages_idempotency_key ON email_messages (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX email_messages_due_idx ON email_messages (next_attempt_at) WHERE status IN ('queued', 'sending');
CREATE INDEX email_messages_recipient_idx ON email_messages (tenant_id, to_address, created_at DESC);
CREATE INDEX email_messages_created_idx ON email_messages (created_at DESC);

-- +goose Down
DROP TABLE email_messages;

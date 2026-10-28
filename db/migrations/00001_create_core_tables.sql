-- Phase 2: tenants, accounts, channel sessions and messages.

-- +goose Up
CREATE TABLE tenants (
    id             UUID         PRIMARY KEY,
    name           VARCHAR(100) NOT NULL,
    -- First characters of the API key, to identify it without storing it.
    api_key_prefix VARCHAR(16)  NOT NULL,
    -- SHA-256 of the API key. The key itself is shown once and never stored.
    api_key_hash   CHAR(64)     NOT NULL,
    -- Needed in clear to sign outgoing webhooks with HMAC-SHA256.
    webhook_secret VARCHAR(64)  NOT NULL,
    is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tenants_api_key_hash_key ON tenants (api_key_hash);

-- A business inside a tenant, identified by its ID in the tenant's own system.
CREATE TABLE accounts (
    id           UUID         PRIMARY KEY,
    tenant_id    UUID         NOT NULL REFERENCES tenants (id),
    external_ref VARCHAR(100) NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX accounts_tenant_external_ref_key ON accounts (tenant_id, external_ref);

-- A channel connection of an account. At most ONE per account and channel.
CREATE TABLE sessions (
    id           UUID         PRIMARY KEY,
    tenant_id    UUID         NOT NULL REFERENCES tenants (id),
    account_id   UUID         NOT NULL REFERENCES accounts (id),
    channel      VARCHAR(20)  NOT NULL,
    status       VARCHAR(20)  NOT NULL,
    -- Identifier of the linked device in the channel (WhatsApp JID). Empty until paired.
    channel_ref  VARCHAR(255) NOT NULL DEFAULT '',
    connected_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT sessions_channel_valid CHECK (channel IN ('whatsapp')),
    CONSTRAINT sessions_status_valid CHECK (status IN ('pending_qr', 'connected', 'disconnected', 'banned'))
);
CREATE UNIQUE INDEX sessions_tenant_account_channel_key ON sessions (tenant_id, account_id, channel);

CREATE TABLE messages (
    id                  UUID          PRIMARY KEY,
    tenant_id           UUID          NOT NULL REFERENCES tenants (id),
    session_id          UUID          NOT NULL REFERENCES sessions (id),
    channel             VARCHAR(20)   NOT NULL,
    recipient           VARCHAR(20)   NOT NULL,
    type                VARCHAR(20)   NOT NULL,
    body                VARCHAR(4096) NOT NULL DEFAULT '',
    attachment_filename VARCHAR(255)  NOT NULL DEFAULT '',
    attachment_mime     VARCHAR(100)  NOT NULL DEFAULT '',
    attachment_size     BIGINT        NOT NULL DEFAULT 0,
    priority            VARCHAR(20)   NOT NULL,
    idempotency_key     VARCHAR(100),
    status              VARCHAR(20)   NOT NULL,
    error               VARCHAR(500)  NOT NULL DEFAULT '',
    provider_message_id VARCHAR(100)  NOT NULL DEFAULT '',
    sent_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ   NOT NULL DEFAULT now(),
    CONSTRAINT messages_type_valid CHECK (type IN ('text', 'image', 'document')),
    CONSTRAINT messages_priority_valid CHECK (priority IN ('transactional', 'promotional')),
    CONSTRAINT messages_status_valid CHECK (status IN ('queued', 'sending', 'sent', 'delivered', 'read', 'failed'))
);
CREATE UNIQUE INDEX messages_tenant_idempotency_key ON messages (tenant_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX messages_session_created_idx ON messages (session_id, created_at);

-- +goose Down
DROP TABLE messages;
DROP TABLE sessions;
DROP TABLE accounts;
DROP TABLE tenants;

-- Phase 5: administration panel users and sessions, separate from tenant API keys.

-- +goose Up
CREATE TABLE admin_users (
    id            UUID         PRIMARY KEY,
    email         VARCHAR(254) NOT NULL,
    password_hash VARCHAR(100) NOT NULL,
    is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ,
    CONSTRAINT admin_users_email_lowercase CHECK (email = lower(email))
);
CREATE UNIQUE INDEX admin_users_email_key ON admin_users (email);

-- Opaque bearer tokens: only the SHA-256 of the token is stored.
CREATE TABLE admin_sessions (
    id         UUID        PRIMARY KEY,
    admin_id   UUID        NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash CHAR(64)    NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admin_sessions_token_hash_key ON admin_sessions (token_hash);
CREATE INDEX admin_sessions_admin_idx ON admin_sessions (admin_id);

-- +goose Down
DROP TABLE admin_sessions;
DROP TABLE admin_users;

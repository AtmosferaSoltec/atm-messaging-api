-- The panel now has a single operator configured in the environment, so the
-- admin user tables are gone. Connect links let a tenant's customer scan a QR
-- code without having any account.

-- +goose Up
DROP TABLE admin_sessions;
DROP TABLE admin_users;

-- One link per session: creating a new one replaces (and so invalidates) the old.
CREATE TABLE connect_links (
    session_id UUID         PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    -- SHA-256 of the token that goes in the URL. The token itself is never stored.
    token_hash CHAR(64)     NOT NULL,
    -- What the customer sees on the page (e.g. the business name).
    label      VARCHAR(100) NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ  NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX connect_links_token_hash_key ON connect_links (token_hash);

-- +goose Down
DROP TABLE connect_links;

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

CREATE TABLE admin_sessions (
    id         UUID        PRIMARY KEY,
    admin_id   UUID        NOT NULL REFERENCES admin_users (id) ON DELETE CASCADE,
    token_hash CHAR(64)    NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX admin_sessions_token_hash_key ON admin_sessions (token_hash);
CREATE INDEX admin_sessions_admin_idx ON admin_sessions (admin_id);

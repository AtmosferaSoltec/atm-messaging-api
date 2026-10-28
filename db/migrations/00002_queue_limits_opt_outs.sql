-- Phase 3: persistent per-session queue, retries, warm-up reference and opt-outs.

-- +goose Up
ALTER TABLE messages
    -- Kept only while the message is pending, so retries and restarts can resend it.
    ADD COLUMN attachment_data  BYTEA,
    ADD COLUMN attempts         INT         NOT NULL DEFAULT 0,
    ADD COLUMN next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now();

-- Deleting a session deletes its messages (queued ones included). This also fixes
-- the phase 2 constraint, which made deleting a session with messages fail.
ALTER TABLE messages DROP CONSTRAINT messages_session_id_fkey;
ALTER TABLE messages ADD CONSTRAINT messages_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES sessions (id) ON DELETE CASCADE;

CREATE INDEX messages_queue_idx ON messages (session_id, next_attempt_at) WHERE status = 'queued';
CREATE INDEX messages_sent_count_idx ON messages (session_id, priority, sent_at) WHERE sent_at IS NOT NULL;

-- Start of the warm-up of a number: its first successful connection.
ALTER TABLE sessions ADD COLUMN first_connected_at TIMESTAMPTZ;
UPDATE sessions SET first_connected_at = connected_at WHERE connected_at IS NOT NULL;

-- Recipients that unsubscribed from promotional messages, per session.
CREATE TABLE opt_outs (
    session_id UUID        NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    recipient  VARCHAR(20) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, recipient)
);

-- +goose Down
DROP TABLE opt_outs;
ALTER TABLE sessions DROP COLUMN first_connected_at;
DROP INDEX messages_sent_count_idx;
DROP INDEX messages_queue_idx;
ALTER TABLE messages DROP CONSTRAINT messages_session_id_fkey;
ALTER TABLE messages ADD CONSTRAINT messages_session_id_fkey
    FOREIGN KEY (session_id) REFERENCES sessions (id);
ALTER TABLE messages DROP COLUMN next_attempt_at, DROP COLUMN attempts, DROP COLUMN attachment_data;

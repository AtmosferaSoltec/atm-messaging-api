-- Creates the role, database and schema of atm-messaging-api in the shared PostgreSQL.
-- Run ONCE, as superuser. It is deliberately not idempotent: if the role or the
-- database already exist it fails instead of overwriting them.
--
--   docker exec -i atm-postgres psql -v ON_ERROR_STOP=1 -U postgres \
--     -v app_password="'<password>'" -d postgres < scripts/provision-db.sql
--
-- The schema also holds the whatsmeow_* tables, which contain the customers'
-- WhatsApp credentials: keep the role confined and treat backups as sensitive.

CREATE ROLE messaging_app WITH LOGIN PASSWORD :app_password;
CREATE DATABASE messaging OWNER messaging_app;

REVOKE CONNECT ON DATABASE messaging FROM PUBLIC;
GRANT CONNECT ON DATABASE messaging TO messaging_app;

\connect messaging

CREATE SCHEMA IF NOT EXISTS messaging AUTHORIZATION messaging_app;

ALTER SCHEMA public OWNER TO postgres;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM messaging_app;

ALTER ROLE messaging_app IN DATABASE messaging SET search_path TO messaging;

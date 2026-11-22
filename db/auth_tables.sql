-- Demo accounts for the plan view (email + password). dock-api creates and updates them from its settings at start;
-- the password is stored only as a bcrypt hash. The nightly demo reset reloads the snapshot but keeps this table.
BEGIN;

CREATE TABLE app_users (
    user_id BIGSERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE CHECK (email = lower(email) AND position('@' IN email) > 1),
    display_name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_login_at TIMESTAMPTZ
);

COMMIT;

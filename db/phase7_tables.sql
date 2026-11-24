-- Phase 7: the driver chat agent's records become planner data (see CLAUDE.md and docs/agent-api.md).
--   coordinator_cases     the escalation queue the agent fills and coordinators work on the Driver cases screen
--   slot_change_requests  a HIGH or CRITICAL truck's slot change waiting for a coordinator; the held slot stays
--                         reserved until the request is approved, rejected, withdrawn or expires
--   booking_changes       the driver feed also carries approval outcomes (kind APPROVAL) and coordinator replies
--                         (kind REPLY), which come from people, not solver runs
-- Applied after agent_tables.sql.
BEGIN;

CREATE TABLE coordinator_cases (
    case_id TEXT PRIMARY KEY,
    facility_id TEXT REFERENCES facilities(facility_id),     -- from the shipment; NULL for an unknown number
    shipment_id TEXT REFERENCES shipments(shipment_id),
    driver_id TEXT REFERENCES drivers(driver_id),
    thread_id TEXT REFERENCES chat_threads(thread_id),
    exception_id TEXT REFERENCES driver_exceptions(exception_id),
    channel TEXT NOT NULL CHECK (channel IN ('TELEGRAM', 'WHATSAPP', 'SIM', 'CONSOLE')),
    channel_user_id TEXT,                                     -- where a reply goes when there is no driver
    phone TEXT,
    reason_code TEXT NOT NULL CHECK (reason_code ~ '^[A-Z][A-Z_]{1,39}$'),
    urgency TEXT NOT NULL CHECK (urgency IN ('LOW', 'NORMAL', 'URGENT')),
    summary TEXT NOT NULL CHECK (length(summary) BETWEEN 1 AND 1000),
    case_status TEXT NOT NULL DEFAULT 'OPEN' CHECK (case_status IN ('OPEN', 'ASSIGNED', 'RESOLVED')),
    created_at TIMESTAMPTZ NOT NULL,
    created_by TEXT NOT NULL,
    assigned_to TEXT,
    assigned_at TIMESTAMPTZ,
    resolved_at TIMESTAMPTZ,
    resolved_by TEXT,
    reply_text TEXT CHECK (reply_text IS NULL OR length(reply_text) BETWEEN 1 AND 1000),
    CHECK ((case_status = 'RESOLVED') = (resolved_at IS NOT NULL)),
    CHECK (case_status <> 'ASSIGNED' OR assigned_to IS NOT NULL)
);

CREATE INDEX ix_cases_open ON coordinator_cases(case_status, urgency, created_at) WHERE case_status <> 'RESOLVED';

CREATE TABLE slot_change_requests (
    request_id TEXT PRIMARY KEY,
    facility_id TEXT NOT NULL REFERENCES facilities(facility_id),
    shipment_id TEXT NOT NULL REFERENCES shipments(shipment_id),
    hold_appointment_id TEXT NOT NULL UNIQUE REFERENCES appointments(appointment_id),
    from_appointment_id TEXT REFERENCES appointments(appointment_id),   -- the booking at request time, if any
    thread_id TEXT REFERENCES chat_threads(thread_id),
    priority_code TEXT NOT NULL,
    request_status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (request_status IN ('PENDING', 'APPROVED', 'REJECTED', 'EXPIRED', 'WITHDRAWN')),
    requested_at TIMESTAMPTZ NOT NULL,
    requested_by TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    decided_at TIMESTAMPTZ,
    decided_by TEXT,
    decision_note TEXT CHECK (decision_note IS NULL OR length(decision_note) <= 500),
    CHECK (expires_at > requested_at),
    CHECK ((request_status = 'PENDING') = (decided_at IS NULL))
);

-- One open request per truck.
CREATE UNIQUE INDEX ux_pending_request_per_shipment ON slot_change_requests(shipment_id)
    WHERE request_status = 'PENDING';

-- When a driver hold stops blocking its slot: a reservation lasts until its request's expiry, any other hold
-- default_min minutes after it was booked. dock-api, the solver's loader and validator V3 all use this rule.
CREATE FUNCTION hold_expires_at(p_appointment_id TEXT, p_booked_at TIMESTAMPTZ, p_default_min BIGINT)
RETURNS TIMESTAMPTZ LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT r.expires_at FROM slot_change_requests r
         WHERE r.hold_appointment_id = p_appointment_id AND r.request_status = 'PENDING'),
        p_booked_at + make_interval(mins => p_default_min::int))
$$;

-- The driver feed: approval outcomes and coordinator replies besides the solver's booking changes.
ALTER TABLE booking_changes ALTER COLUMN run_id DROP NOT NULL;
ALTER TABLE booking_changes ALTER COLUMN shipment_id DROP NOT NULL;
ALTER TABLE booking_changes ALTER COLUMN facility_id DROP NOT NULL;
ALTER TABLE booking_changes DROP CONSTRAINT booking_changes_action_check;
ALTER TABLE booking_changes DROP CONSTRAINT booking_changes_check;
ALTER TABLE booking_changes
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'BOOKING' CHECK (kind IN ('BOOKING', 'APPROVAL', 'REPLY')),
    ADD COLUMN request_id TEXT REFERENCES slot_change_requests(request_id),
    ADD COLUMN case_id TEXT REFERENCES coordinator_cases(case_id),
    ADD COLUMN message_text TEXT CHECK (message_text IS NULL OR length(message_text) BETWEEN 1 AND 1000),
    ADD CONSTRAINT booking_changes_kind_action CHECK (
        (kind = 'BOOKING' AND action IN ('NEW', 'MOVED', 'RELEASED') AND run_id IS NOT NULL
             AND shipment_id IS NOT NULL AND (action = 'RELEASED') = (to_slot_id IS NULL))
        OR (kind = 'APPROVAL' AND action IN ('APPROVED', 'REJECTED', 'EXPIRED') AND request_id IS NOT NULL
             AND shipment_id IS NOT NULL)
        OR (kind = 'REPLY' AND action = 'REPLY' AND case_id IS NOT NULL AND message_text IS NOT NULL));

-- A webhook retry or a re-sent Telegram update is stored once.
CREATE UNIQUE INDEX ux_chat_messages_external ON chat_messages(thread_id, external_message_id)
    WHERE external_message_id IS NOT NULL;

COMMIT;

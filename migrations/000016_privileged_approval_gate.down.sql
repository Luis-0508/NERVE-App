-- Fails while a command is awaiting approval: the restored status check does
-- not know that state. Cancel such commands first.
DROP TABLE IF EXISTS remote_scopes;
DROP TRIGGER IF EXISTS remote_commands_insert_guard ON remote_commands;
DROP FUNCTION IF EXISTS guard_remote_command_insert();

CREATE OR REPLACE FUNCTION guard_remote_command_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.id <> OLD.id OR NEW.provider <> OLD.provider OR NEW.agent_id <> OLD.agent_id
       OR NEW.command_type <> OLD.command_type OR NEW.risk <> OLD.risk OR NEW.parameters <> OLD.parameters
       OR NEW.requested_by <> OLD.requested_by OR NEW.requested_at <> OLD.requested_at
       OR NEW.expires_at <> OLD.expires_at OR NEW.timeout_seconds <> OLD.timeout_seconds
       OR NEW.max_output_bytes <> OLD.max_output_bytes THEN
        RAISE EXCEPTION 'remote command request fields are immutable';
    END IF;
    IF OLD.status IN ('succeeded', 'failed', 'timed_out', 'cancelled', 'expired') THEN
        RAISE EXCEPTION 'remote command % is already %', OLD.id, OLD.status;
    END IF;
    IF NOT (
        (OLD.status = 'queued' AND NEW.status IN ('claimed', 'cancelled', 'expired')) OR
        (OLD.status = 'claimed' AND NEW.status IN ('running', 'succeeded', 'failed', 'timed_out', 'cancelled')) OR
        (OLD.status = 'running' AND NEW.status IN ('succeeded', 'failed', 'timed_out', 'cancelled'))
    ) THEN
        RAISE EXCEPTION 'remote command transition % -> % is not allowed', OLD.status, NEW.status;
    END IF;
    IF OLD.claimed_at IS NOT NULL AND (NEW.claimed_at IS DISTINCT FROM OLD.claimed_at OR NEW.claimed_by_credential IS DISTINCT FROM OLD.claimed_by_credential) THEN
        RAISE EXCEPTION 'remote command claim is immutable';
    END IF;
    RETURN NEW;
END;
$$;

DROP TABLE IF EXISTS remote_command_approvals;
DROP INDEX IF EXISTS remote_commands_awaiting_approval_idx;
ALTER TABLE remote_commands DROP CONSTRAINT IF EXISTS remote_commands_awaiting_approval_check;
ALTER TABLE remote_commands DROP CONSTRAINT remote_commands_status_check;
ALTER TABLE remote_commands ADD CONSTRAINT remote_commands_status_check CHECK (status IN (
    'queued', 'claimed', 'running', 'succeeded', 'failed', 'timed_out', 'cancelled', 'expired'
));
ALTER TABLE agents DROP COLUMN IF EXISTS transport_reported_at, DROP COLUMN IF EXISTS transport;

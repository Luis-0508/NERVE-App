-- Approval gate for privileged remote commands (#99). No privileged command
-- type is enabled yet; this adds the preconditions the gate checks and the
-- state a privileged command passes through before an agent may claim it.

-- Transport the agent reports in its signed heartbeat. 'https' means the agent
-- talks to an https API URL with certificate verification (it never disables
-- verification). NULL: the agent predates transport reporting.
ALTER TABLE agents
    ADD COLUMN transport text CHECK (transport IN ('http', 'https')),
    ADD COLUMN transport_reported_at timestamptz;

-- A privileged command starts in 'awaiting_approval' and is queued only by an
-- operator's approval.
ALTER TABLE remote_commands DROP CONSTRAINT remote_commands_status_check;
ALTER TABLE remote_commands ADD CONSTRAINT remote_commands_status_check CHECK (status IN (
    'awaiting_approval', 'queued', 'claimed', 'running', 'succeeded', 'failed', 'timed_out', 'cancelled', 'expired'
));
ALTER TABLE remote_commands ADD CONSTRAINT remote_commands_awaiting_approval_check
    CHECK (status <> 'awaiting_approval' OR (claimed_at IS NULL AND started_at IS NULL AND completed_at IS NULL));
CREATE INDEX remote_commands_awaiting_approval_idx ON remote_commands(expires_at) WHERE status = 'awaiting_approval';

-- Core-signed approvals. Write-once and never deleted: the agent verifies the
-- signature, and the row proves who approved what.
CREATE TABLE remote_command_approvals (
    command_id uuid PRIMARY KEY REFERENCES remote_commands(id) ON DELETE RESTRICT,
    approved_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    approved_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    parameters_digest text NOT NULL CHECK (parameters_digest ~ '^[0-9a-f]{64}$'),
    key_id text NOT NULL CHECK (key_id ~ '^[0-9a-f]{16}$'),
    signature text NOT NULL CHECK (length(signature) BETWEEN 1 AND 128),
    CHECK (expires_at > approved_at AND expires_at <= approved_at + interval '1 hour')
);
CREATE INDEX remote_command_approvals_expiry_idx ON remote_command_approvals(expires_at);

CREATE TRIGGER remote_command_approvals_write_once
BEFORE UPDATE OR DELETE ON remote_command_approvals
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();

-- Privileged and interactive commands can never be inserted in a claimable
-- state: they reach 'queued' only through an approval.
CREATE FUNCTION guard_remote_command_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.risk <> 'read_only' AND NEW.status IN ('queued', 'claimed', 'running') THEN
        RAISE EXCEPTION 'a % remote command must start awaiting approval', NEW.risk;
    END IF;
    IF NEW.risk = 'read_only' AND NEW.status = 'awaiting_approval' THEN
        RAISE EXCEPTION 'read-only remote commands need no approval';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER remote_commands_insert_guard
BEFORE INSERT ON remote_commands
FOR EACH ROW EXECUTE FUNCTION guard_remote_command_insert();

-- The transition guard from 000007, extended with 'awaiting_approval'. Leaving
-- it requires a stored approval, and a non-read-only command can be claimed
-- only while that approval is unexpired.
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
        (OLD.status = 'awaiting_approval' AND NEW.status IN ('queued', 'cancelled', 'expired')) OR
        (OLD.status = 'queued' AND NEW.status IN ('claimed', 'cancelled', 'expired')) OR
        (OLD.status = 'claimed' AND NEW.status IN ('running', 'succeeded', 'failed', 'timed_out', 'cancelled')) OR
        (OLD.status = 'running' AND NEW.status IN ('succeeded', 'failed', 'timed_out', 'cancelled'))
    ) THEN
        RAISE EXCEPTION 'remote command transition % -> % is not allowed', OLD.status, NEW.status;
    END IF;
    IF OLD.status = 'awaiting_approval' AND NEW.status = 'queued'
       AND NOT EXISTS (SELECT 1 FROM remote_command_approvals WHERE command_id = NEW.id) THEN
        RAISE EXCEPTION 'remote command % has no approval', NEW.id;
    END IF;
    IF NEW.status = 'claimed' AND OLD.status = 'queued' AND NEW.risk <> 'read_only'
       AND NOT EXISTS (SELECT 1 FROM remote_command_approvals WHERE command_id = NEW.id AND expires_at > now()) THEN
        RAISE EXCEPTION 'remote command % has no valid approval', NEW.id;
    END IF;
    IF OLD.claimed_at IS NOT NULL AND (NEW.claimed_at IS DISTINCT FROM OLD.claimed_at OR NEW.claimed_by_credential IS DISTINCT FROM OLD.claimed_by_credential) THEN
        RAISE EXCEPTION 'remote command claim is immutable';
    END IF;
    RETURN NEW;
END;
$$;

-- Who may request and approve which privileged command types on which
-- devices. A scope names exactly one subject (a role or a user) and at most
-- one target (a device or a device tag; neither means every device). The
-- command type '*' matches every type. Scopes apply to privileged and
-- interactive actions, including future remote sessions (decision 0006);
-- read-only commands need only their permission.
CREATE TABLE remote_scopes (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    role_id uuid REFERENCES roles(id) ON DELETE CASCADE,
    user_id uuid REFERENCES users(id) ON DELETE CASCADE,
    device_id uuid REFERENCES devices(entity_id) ON DELETE CASCADE,
    device_tag text CHECK (device_tag IS NULL OR device_tag ~ '^[a-z0-9][a-z0-9._:-]{0,47}$'),
    command_types text[] NOT NULL CHECK (cardinality(command_types) BETWEEN 1 AND 64
        AND array_position(command_types, NULL) IS NULL),
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((role_id IS NULL) <> (user_id IS NULL)),
    CHECK (device_id IS NULL OR device_tag IS NULL)
);
CREATE INDEX remote_scopes_role_idx ON remote_scopes(role_id) WHERE role_id IS NOT NULL;
CREATE INDEX remote_scopes_user_idx ON remote_scopes(user_id) WHERE user_id IS NOT NULL;

CREATE TRIGGER remote_scopes_not_merged BEFORE INSERT OR UPDATE OF device_id ON remote_scopes
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');

-- Administrators keep their current reach: every privileged type on every device.
INSERT INTO remote_scopes (role_id, command_types)
SELECT id, ARRAY['*'] FROM roles WHERE key = 'administrator';

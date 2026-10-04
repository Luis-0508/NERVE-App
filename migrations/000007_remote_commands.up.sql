-- Provider-neutral remote command pipeline. A command is delivered at most
-- once: claiming moves it out of 'queued' atomically and no transition ever
-- returns a command to 'queued'. Output lives in remote_command_results so the
-- immutable audit log only references it.
CREATE TABLE remote_commands (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider text NOT NULL DEFAULT 'nerve-agent' CHECK (provider IN ('nerve-agent')),
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    device_id uuid REFERENCES devices(entity_id) ON DELETE SET NULL,
    command_type text NOT NULL CHECK (command_type ~ '^[a-z]+(\.[a-z_]+)+$'),
    risk text NOT NULL CHECK (risk IN ('read_only', 'privileged', 'interactive')),
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(parameters) = 'object' AND octet_length(parameters::text) <= 4096),
    requested_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    requested_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    timeout_seconds integer NOT NULL CHECK (timeout_seconds BETWEEN 1 AND 3600),
    max_output_bytes integer NOT NULL CHECK (max_output_bytes BETWEEN 1024 AND 1048576),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued', 'claimed', 'running', 'succeeded', 'failed', 'timed_out', 'cancelled', 'expired'
    )),
    claimed_at timestamptz,
    claimed_by_credential uuid REFERENCES agent_credentials(id) ON DELETE RESTRICT,
    started_at timestamptz,
    completed_at timestamptz,
    exit_code integer,
    failure_code text CHECK (failure_code IS NULL OR failure_code ~ '^[a-z_]{1,64}$'),
    failure_reason text CHECK (failure_reason IS NULL OR length(failure_reason) <= 1000),
    cancelled_by uuid REFERENCES users(id) ON DELETE RESTRICT,
    CHECK (expires_at > requested_at AND expires_at <= requested_at + interval '1 day'),
    CHECK (status <> 'queued' OR (claimed_at IS NULL AND started_at IS NULL AND completed_at IS NULL)),
    CHECK (status NOT IN ('claimed', 'running') OR (claimed_at IS NOT NULL AND completed_at IS NULL)),
    CHECK (status <> 'running' OR started_at IS NOT NULL),
    CHECK (status NOT IN ('succeeded', 'failed', 'timed_out', 'cancelled', 'expired') OR completed_at IS NOT NULL),
    CHECK (status NOT IN ('succeeded', 'failed') OR claimed_at IS NOT NULL),
    CHECK (status <> 'expired' OR claimed_at IS NULL),
    CHECK ((claimed_at IS NULL) = (claimed_by_credential IS NULL)),
    CHECK ((status = 'cancelled') = (cancelled_by IS NOT NULL))
);
CREATE INDEX remote_commands_agent_queue_idx ON remote_commands(agent_id, requested_at) WHERE status = 'queued';
CREATE INDEX remote_commands_active_idx ON remote_commands(status, claimed_at) WHERE status IN ('queued', 'claimed', 'running');
CREATE INDEX remote_commands_agent_time_idx ON remote_commands(agent_id, requested_at DESC);
CREATE INDEX remote_commands_device_time_idx ON remote_commands(device_id, requested_at DESC) WHERE device_id IS NOT NULL;

CREATE FUNCTION guard_remote_command_update() RETURNS trigger LANGUAGE plpgsql AS $$
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

CREATE TRIGGER remote_commands_guard
BEFORE UPDATE ON remote_commands
FOR EACH ROW EXECUTE FUNCTION guard_remote_command_update();

CREATE TRIGGER remote_commands_no_delete
BEFORE DELETE ON remote_commands
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();

-- Bounded command output. Rows may be pruned by retention; the command row
-- and its audit records remain.
CREATE TABLE remote_command_results (
    command_id uuid PRIMARY KEY REFERENCES remote_commands(id) ON DELETE CASCADE,
    output jsonb,
    stdout text,
    stderr text,
    output_truncated boolean NOT NULL DEFAULT false,
    stdout_truncated boolean NOT NULL DEFAULT false,
    stderr_truncated boolean NOT NULL DEFAULT false,
    output_bytes integer NOT NULL CHECK (output_bytes BETWEEN 0 AND 1048576),
    result_digest text NOT NULL CHECK (result_digest ~ '^[0-9a-f]{64}$'),
    submitted_at timestamptz NOT NULL DEFAULT now(),
    CHECK (octet_length(COALESCE(stdout, '')) <= 1048576 AND octet_length(COALESCE(stderr, '')) <= 1048576)
);
CREATE INDEX remote_command_results_submitted_idx ON remote_command_results(submitted_at);

CREATE FUNCTION guard_remote_command_result_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'remote command results are write-once';
END;
$$;

CREATE TRIGGER remote_command_results_write_once
BEFORE UPDATE ON remote_command_results
FOR EACH ROW EXECUTE FUNCTION guard_remote_command_result_update();

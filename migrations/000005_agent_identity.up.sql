-- Explicit, extensible permissions. Roles map to permissions in the database so
-- authorization never depends on a hard-coded "administrator can do anything".
CREATE TABLE permissions (
    key text PRIMARY KEY CHECK (key ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$'),
    description text NOT NULL
);

CREATE TABLE role_permissions (
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission_key text NOT NULL REFERENCES permissions(key) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_key)
);

INSERT INTO permissions (key, description) VALUES
    ('discovery.manage', 'Configure discovery targets and start or stop scans'),
    ('agents.view', 'View enrolled management agents, their status and capabilities'),
    ('agents.manage', 'Create enrollments, revoke, remove and associate management agents'),
    ('remote.view', 'View remote-management command history and results'),
    ('remote.execute.read_only', 'Run read-only remote-management commands'),
    ('remote.execute.privileged', 'Run privileged remote-management commands (none are enabled yet)'),
    ('audit.view', 'Read the immutable audit log');

INSERT INTO role_permissions (role_id, permission_key)
SELECT role.id, permission.key
FROM roles role
JOIN permissions permission ON
    role.key = 'administrator'
    OR (role.key = 'technician' AND permission.key IN ('agents.view', 'remote.view', 'remote.execute.read_only'))
    OR (role.key = 'read_only' AND permission.key IN ('agents.view'));

-- One row per enrolled management agent. The identity is stable across
-- credential rotation and re-enrollment. Agents are never hard-deleted because
-- audit records and command history reference them; removal is a final state.
CREATE TABLE agents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked', 'removed')),
    hostname text,
    os text,
    os_version text,
    architecture text,
    agent_version text,
    machine_fingerprint text,
    reported_mac_addresses macaddr[] NOT NULL DEFAULT '{}',
    reported_ip_addresses inet[] NOT NULL DEFAULT '{}',
    capabilities text[] NOT NULL DEFAULT '{}',
    facts_digest text,
    last_error text,
    last_ip inet,
    first_enrolled_at timestamptz NOT NULL DEFAULT now(),
    enrolled_at timestamptz NOT NULL DEFAULT now(),
    last_heartbeat_at timestamptz,
    last_seen_at timestamptz,
    device_id uuid REFERENCES devices(entity_id) ON DELETE SET NULL,
    association_status text NOT NULL DEFAULT 'unassociated'
        CHECK (association_status IN ('unassociated', 'candidate', 'automatic', 'confirmed')),
    association_evidence jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(association_evidence) = 'array'),
    associated_at timestamptz,
    associated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    revoked_at timestamptz,
    removed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((device_id IS NOT NULL) = (association_status IN ('automatic', 'confirmed'))),
    CHECK ((status = 'removed') = (removed_at IS NOT NULL)),
    CHECK (status <> 'removed' OR device_id IS NULL)
);
-- A device has at most one live native agent. A reinstall that enrolls a second
-- agent for the same host becomes an association candidate, never a silent merge.
CREATE UNIQUE INDEX agents_one_live_agent_per_device_idx ON agents(device_id)
WHERE device_id IS NOT NULL AND status <> 'removed';
CREATE INDEX agents_status_seen_idx ON agents(status, last_seen_at DESC);

-- Agent credentials are Ed25519 public keys. The private key never leaves the
-- endpoint, so the database holds nothing that can authenticate as an agent.
CREATE TABLE agent_credentials (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id uuid NOT NULL REFERENCES agents(id) ON DELETE RESTRICT,
    algorithm text NOT NULL CHECK (algorithm = 'ed25519'),
    public_key bytea NOT NULL CHECK (octet_length(public_key) = 32),
    fingerprint text NOT NULL UNIQUE CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    revoked_by uuid REFERENCES users(id) ON DELETE SET NULL,
    revocation_reason text CHECK (revocation_reason IS NULL OR revocation_reason IN ('revoked', 'reenrolled', 'removed')),
    CHECK ((revoked_at IS NULL) = (revocation_reason IS NULL))
);
CREATE UNIQUE INDEX agent_credentials_one_active_idx ON agent_credentials(agent_id) WHERE revoked_at IS NULL;

CREATE FUNCTION guard_agent_credential_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.agent_id <> OLD.agent_id OR NEW.public_key <> OLD.public_key
       OR NEW.fingerprint <> OLD.fingerprint OR NEW.algorithm <> OLD.algorithm
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'agent credential key material is immutable';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
        RAISE EXCEPTION 'agent credential revocation is final';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_credentials_guard
BEFORE UPDATE ON agent_credentials
FOR EACH ROW EXECUTE FUNCTION guard_agent_credential_update();

CREATE TRIGGER agent_credentials_no_delete
BEFORE DELETE ON agent_credentials
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();

-- Short-lived, single-use enrollment tokens. Only the SHA-256 digest of the
-- 256-bit secret is stored; the token is shown to the operator exactly once.
CREATE TABLE agent_enrollments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    display_name text CHECK (display_name IS NULL OR length(display_name) BETWEEN 1 AND 200),
    device_id uuid REFERENCES devices(entity_id) ON DELETE SET NULL,
    target_agent_id uuid REFERENCES agents(id) ON DELETE RESTRICT,
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    used_from inet,
    enrolled_agent_id uuid REFERENCES agents(id) ON DELETE RESTRICT,
    revoked_at timestamptz,
    revoked_by uuid REFERENCES users(id) ON DELETE SET NULL,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '24 hours'),
    CHECK ((used_at IS NULL) = (enrolled_agent_id IS NULL)),
    CHECK (used_at IS NULL OR revoked_at IS NULL),
    CHECK (target_agent_id IS NULL OR enrolled_agent_id IS NULL OR target_agent_id = enrolled_agent_id)
);
CREATE INDEX agent_enrollments_created_idx ON agent_enrollments(created_at DESC);

CREATE FUNCTION guard_agent_enrollment_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.secret_hash <> OLD.secret_hash OR NEW.created_by <> OLD.created_by
       OR NEW.created_at <> OLD.created_at OR NEW.expires_at <> OLD.expires_at
       OR NEW.target_agent_id IS DISTINCT FROM OLD.target_agent_id THEN
        RAISE EXCEPTION 'agent enrollment terms are immutable';
    END IF;
    IF OLD.used_at IS NOT NULL AND (NEW.used_at IS DISTINCT FROM OLD.used_at OR NEW.enrolled_agent_id IS DISTINCT FROM OLD.enrolled_agent_id) THEN
        RAISE EXCEPTION 'agent enrollment was already used';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
        RAISE EXCEPTION 'agent enrollment revocation is final';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_enrollments_guard
BEFORE UPDATE ON agent_enrollments
FOR EACH ROW EXECUTE FUNCTION guard_agent_enrollment_update();

-- Replay protection for signed agent requests. Rows only need to outlive the
-- accepted clock-skew window and are pruned by the API maintenance loop.
CREATE TABLE agent_request_nonces (
    credential_id uuid NOT NULL REFERENCES agent_credentials(id) ON DELETE CASCADE,
    nonce bytea NOT NULL CHECK (octet_length(nonce) = 16),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (credential_id, nonce)
);
CREATE INDEX agent_request_nonces_expiry_idx ON agent_request_nonces(expires_at);

-- Audit records can now name an authenticated agent as the actor.
ALTER TABLE audit_log ADD COLUMN actor_agent_id uuid REFERENCES agents(id);
CREATE INDEX audit_log_target_idx ON audit_log(target_type, target_id, occurred_at DESC);
CREATE INDEX audit_log_agent_time_idx ON audit_log(actor_agent_id, occurred_at DESC) WHERE actor_agent_id IS NOT NULL;

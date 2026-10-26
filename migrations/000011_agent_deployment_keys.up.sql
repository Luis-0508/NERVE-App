-- Reusable deployment keys enroll many endpoints with one secret, for example
-- in a GPO or Intune rollout. Only the SHA-256 digest of the 256-bit secret is
-- stored. A key is limited by use count and lifetime and can be revoked; with
-- manual approval, agents it enrolls wait in the 'pending' state.
CREATE TABLE agent_deployment_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
    approval text NOT NULL DEFAULT 'manual' CHECK (approval IN ('manual', 'automatic')),
    max_uses integer NOT NULL CHECK (max_uses BETWEEN 1 AND 10000),
    used_count integer NOT NULL DEFAULT 0 CHECK (used_count BETWEEN 0 AND max_uses),
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    last_used_at timestamptz,
    revoked_at timestamptz,
    revoked_by uuid REFERENCES users(id) ON DELETE SET NULL,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '90 days')
);
CREATE INDEX agent_deployment_keys_created_idx ON agent_deployment_keys(created_at DESC);

CREATE FUNCTION guard_agent_deployment_key_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.secret_hash <> OLD.secret_hash OR NEW.name <> OLD.name OR NEW.approval <> OLD.approval
       OR NEW.max_uses <> OLD.max_uses OR NEW.created_by <> OLD.created_by
       OR NEW.created_at <> OLD.created_at OR NEW.expires_at <> OLD.expires_at THEN
        RAISE EXCEPTION 'agent deployment key terms are immutable';
    END IF;
    IF NEW.used_count < OLD.used_count THEN
        RAISE EXCEPTION 'agent deployment key use count cannot decrease';
    END IF;
    IF OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS DISTINCT FROM OLD.revoked_at THEN
        RAISE EXCEPTION 'agent deployment key revocation is final';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_deployment_keys_guard
BEFORE UPDATE ON agent_deployment_keys
FOR EACH ROW EXECUTE FUNCTION guard_agent_deployment_key_update();

-- 'pending' agents may authenticate and heartbeat, but get no commands and no
-- device association until an operator approves them.
ALTER TABLE agents DROP CONSTRAINT agents_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_status_check CHECK (status IN ('pending', 'active', 'revoked', 'removed'));
ALTER TABLE agents ADD COLUMN deployment_key_id uuid REFERENCES agent_deployment_keys(id) ON DELETE RESTRICT;
CREATE INDEX agents_deployment_key_idx ON agents(deployment_key_id) WHERE deployment_key_id IS NOT NULL;

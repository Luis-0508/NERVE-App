DROP INDEX IF EXISTS agents_deployment_key_idx;
ALTER TABLE agents DROP COLUMN IF EXISTS deployment_key_id;
-- Pending agents cannot be represented without this migration: retire them.
UPDATE agent_credentials SET revoked_at = now(), revocation_reason = 'revoked'
WHERE revoked_at IS NULL AND agent_id IN (SELECT id FROM agents WHERE status = 'pending');
UPDATE agents SET status = 'revoked', revoked_at = COALESCE(revoked_at, now()) WHERE status = 'pending';
ALTER TABLE agents DROP CONSTRAINT IF EXISTS agents_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_status_check CHECK (status IN ('active', 'revoked', 'removed'));
DROP TABLE IF EXISTS agent_deployment_keys;
DROP FUNCTION IF EXISTS guard_agent_deployment_key_update();

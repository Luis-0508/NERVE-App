DROP INDEX IF EXISTS agent_credentials_agent_idx;
-- Rotated credentials stay revoked; only the reason loses its detail.
UPDATE agent_credentials SET revocation_reason = 'revoked' WHERE revocation_reason = 'rotated';
ALTER TABLE agent_credentials DROP CONSTRAINT IF EXISTS agent_credentials_revocation_reason_check;
ALTER TABLE agent_credentials ADD CONSTRAINT agent_credentials_revocation_reason_check
    CHECK (revocation_reason IS NULL OR revocation_reason IN ('revoked', 'reenrolled', 'removed'));

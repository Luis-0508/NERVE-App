-- Agents rotate their own Ed25519 key (K-10): the old credential is revoked
-- with reason 'rotated' in the same transaction that adds the new one.
ALTER TABLE agent_credentials DROP CONSTRAINT agent_credentials_revocation_reason_check;
ALTER TABLE agent_credentials ADD CONSTRAINT agent_credentials_revocation_reason_check
    CHECK (revocation_reason IS NULL OR revocation_reason IN ('revoked', 'reenrolled', 'removed', 'rotated'));
-- The agent list counts each agent's rotations.
CREATE INDEX agent_credentials_agent_idx ON agent_credentials(agent_id);

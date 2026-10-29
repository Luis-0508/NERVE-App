DROP INDEX IF EXISTS discovery_agents_agent_id_idx;
ALTER TABLE discovery_agents DROP COLUMN IF EXISTS agent_id;

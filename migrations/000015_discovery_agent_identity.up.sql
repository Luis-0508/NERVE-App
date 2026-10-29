-- Discovery workers authenticate with their enrolled agent identity (K-9).
-- An enrolled worker's row uses the agent ID as its text key and links the
-- agent; rows without agent_id come from the deprecated shared-token path and
-- stay readable so their scan history is kept.
ALTER TABLE discovery_agents ADD COLUMN agent_id uuid REFERENCES agents(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX discovery_agents_agent_id_idx ON discovery_agents(agent_id) WHERE agent_id IS NOT NULL;

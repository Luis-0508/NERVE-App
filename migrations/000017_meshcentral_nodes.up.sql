-- MeshCentral provider (#104). A binding between a MeshCentral node and a NERVE
-- device is a device identity of type 'meshcentral' whose value is the node ID.
-- It is written only after an operator confirms MAC evidence, so merge and
-- split move it like any other identity.
ALTER TABLE device_identities DROP CONSTRAINT device_identities_identity_type_check;
ALTER TABLE device_identities ADD CONSTRAINT device_identities_identity_type_check
    CHECK (identity_type IN ('mac', 'hostname', 'serial', 'agent', 'snmp', 'hypervisor', 'docker', 'manual', 'ip', 'machine', 'meshcentral'));

-- Last observed state of every node the service account could list. Node IDs
-- derive from the MeshAgent key, so a reinstall yields a new row; rows are
-- never deleted, only marked absent, so a binding to a vanished node stays
-- visible until an operator removes it.
CREATE TABLE meshcentral_nodes (
    node_id text PRIMARY KEY CHECK (node_id ~ '^node/[A-Za-z0-9._@$-]*/[A-Za-z0-9._@$-]+$'),
    mesh_id text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    hostname text NOT NULL DEFAULT '',
    os text NOT NULL DEFAULT '',
    agent_connected boolean NOT NULL DEFAULT false,
    mac_addresses text[] NOT NULL DEFAULT '{}',
    ip_addresses text[] NOT NULL DEFAULT '{}',
    last_connect_at timestamptz,
    present boolean NOT NULL DEFAULT true,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_synced_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX meshcentral_nodes_mac_idx ON meshcentral_nodes USING gin (mac_addresses) WHERE present;
CREATE INDEX meshcentral_nodes_hostname_idx ON meshcentral_nodes (hostname) WHERE present;

-- Outcome of the latest sync, shown to operators without exposing credentials.
CREATE TABLE meshcentral_sync_state (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    last_attempt_at timestamptz,
    last_success_at timestamptz,
    last_error text,
    node_count integer NOT NULL DEFAULT 0
);
INSERT INTO meshcentral_sync_state (id) VALUES (true);

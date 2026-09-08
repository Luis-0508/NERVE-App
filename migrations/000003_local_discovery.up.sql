CREATE TABLE discovery_agents (
    id text PRIMARY KEY,
    hostname text NOT NULL,
    platform text NOT NULL,
    version text NOT NULL,
    capabilities text[] NOT NULL DEFAULT '{}',
    last_heartbeat_at timestamptz NOT NULL DEFAULT now(),
    last_error text,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discovery_targets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL,
    cidr cidr NOT NULL UNIQUE,
    enabled boolean NOT NULL DEFAULT true,
    created_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE discovery_scans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    target_id uuid NOT NULL REFERENCES discovery_targets(id) ON DELETE CASCADE,
    requested_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    agent_id text REFERENCES discovery_agents(id) ON DELETE SET NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'partial', 'failed')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    addresses_total integer NOT NULL DEFAULT 0 CHECK (addresses_total >= 0),
    addresses_reachable integer NOT NULL DEFAULT 0 CHECK (addresses_reachable >= 0),
    error_count integer NOT NULL DEFAULT 0 CHECK (error_count >= 0),
    error_message text
);
CREATE INDEX discovery_scans_target_time_idx ON discovery_scans(target_id, requested_at DESC);
CREATE INDEX discovery_scans_status_time_idx ON discovery_scans(status, requested_at);

CREATE TABLE discovery_scan_errors (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scan_id uuid NOT NULL REFERENCES discovery_scans(id) ON DELETE CASCADE,
    address inet,
    stage text NOT NULL,
    message text NOT NULL,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX discovery_scan_errors_scan_idx ON discovery_scan_errors(scan_id, id);

CREATE TABLE discovery_bindings (
    target_id uuid NOT NULL REFERENCES discovery_targets(id) ON DELETE CASCADE,
    address inet NOT NULL,
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    interface_id uuid NOT NULL REFERENCES network_interfaces(entity_id) ON DELETE CASCADE,
    ip_address_id uuid NOT NULL REFERENCES ip_addresses(entity_id) ON DELETE CASCADE,
    mac_address macaddr,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (target_id, address)
);
CREATE INDEX discovery_bindings_device_idx ON discovery_bindings(device_id, last_seen_at DESC);

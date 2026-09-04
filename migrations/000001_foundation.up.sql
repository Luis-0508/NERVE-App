CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE roles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    key text NOT NULL UNIQUE CHECK (key IN ('administrator', 'technician', 'read_only')),
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO roles (key, name) VALUES
    ('administrator', 'Administrator'),
    ('technician', 'Technician'),
    ('read_only', 'Read-only')
ON CONFLICT (key) DO NOTHING;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    display_name text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    disabled_at timestamptz
);

CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id uuid NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);

CREATE TABLE sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    CHECK (expires_at > created_at)
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE entities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN (
        'device', 'network_interface', 'ip_address', 'subnet', 'physical_port',
        'service', 'service_endpoint', 'hypervisor', 'virtual_machine',
        'container_host', 'container', 'container_network', 'remote_endpoint'
    )),
    name text NOT NULL,
    status text NOT NULL DEFAULT 'unknown' CHECK (status IN ('online', 'offline', 'warning', 'unknown', 'stopped', 'starting')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (last_seen_at >= first_seen_at)
);
CREATE INDEX entities_kind_status_idx ON entities(kind, status);
CREATE INDEX entities_last_seen_idx ON entities(last_seen_at DESC);

CREATE TABLE devices (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    classification text NOT NULL DEFAULT 'unknown',
    hostname text,
    manufacturer text,
    model text,
    serial_number text,
    operating_system text,
    owner text,
    location text,
    approved_at timestamptz,
    notes text
);

CREATE TABLE device_identities (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    identity_type text NOT NULL CHECK (identity_type IN ('mac', 'hostname', 'serial', 'agent', 'snmp', 'hypervisor', 'docker', 'manual')),
    value text NOT NULL,
    source text NOT NULL,
    confidence smallint NOT NULL CHECK (confidence BETWEEN 0 AND 100),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    manually_confirmed_at timestamptz,
    UNIQUE (identity_type, value, device_id)
);
CREATE INDEX device_identities_lookup_idx ON device_identities(identity_type, value);

CREATE TABLE network_interfaces (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    name text NOT NULL,
    mac_address macaddr,
    media_type text,
    mtu integer CHECK (mtu IS NULL OR mtu > 0),
    speed_bps bigint CHECK (speed_bps IS NULL OR speed_bps >= 0),
    is_up boolean,
    UNIQUE (device_id, name)
);

CREATE TABLE ip_addresses (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    address inet NOT NULL,
    version smallint GENERATED ALWAYS AS (family(address)) STORED CHECK (version IN (4, 6)),
    is_primary boolean NOT NULL DEFAULT false,
    valid_from timestamptz NOT NULL DEFAULT now(),
    valid_until timestamptz
);
CREATE INDEX ip_addresses_address_idx ON ip_addresses(address);

CREATE TABLE interface_addresses (
    interface_id uuid NOT NULL REFERENCES network_interfaces(entity_id) ON DELETE CASCADE,
    ip_address_id uuid NOT NULL REFERENCES ip_addresses(entity_id) ON DELETE CASCADE,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (interface_id, ip_address_id)
);

CREATE TABLE subnets (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    cidr cidr NOT NULL UNIQUE,
    gateway_address inet,
    discovery_enabled boolean NOT NULL DEFAULT false,
    discovery_interval_seconds integer CHECK (discovery_interval_seconds IS NULL OR discovery_interval_seconds >= 60)
);

CREATE TABLE physical_ports (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    interface_id uuid REFERENCES network_interfaces(entity_id) ON DELETE SET NULL,
    label text NOT NULL,
    port_index integer,
    speed_bps bigint,
    vlan_id integer CHECK (vlan_id IS NULL OR vlan_id BETWEEN 1 AND 4094),
    poe_state text,
    UNIQUE (device_id, label)
);

CREATE TABLE services (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    service_type text,
    description text,
    health_status text NOT NULL DEFAULT 'unknown' CHECK (health_status IN ('healthy', 'unhealthy', 'unknown'))
);

CREATE TABLE service_endpoints (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    service_id uuid REFERENCES services(entity_id) ON DELETE CASCADE,
    host_entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    protocol text NOT NULL CHECK (protocol IN ('tcp', 'udp')),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    exposure text NOT NULL DEFAULT 'unknown' CHECK (exposure IN ('local', 'lan', 'internet', 'unknown')),
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed', 'filtered', 'unknown')),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (host_entity_id, protocol, port)
);

CREATE TABLE hypervisors (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    platform text NOT NULL,
    external_id text,
    UNIQUE (platform, external_id)
);

CREATE TABLE virtual_machines (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    hypervisor_id uuid NOT NULL REFERENCES hypervisors(entity_id) ON DELETE CASCADE,
    external_id text NOT NULL,
    cpu_count integer,
    memory_bytes bigint,
    state text NOT NULL DEFAULT 'unknown',
    UNIQUE (hypervisor_id, external_id)
);

CREATE TABLE container_hosts (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    device_id uuid REFERENCES devices(entity_id) ON DELETE SET NULL,
    virtual_machine_id uuid REFERENCES virtual_machines(entity_id) ON DELETE SET NULL,
    runtime text NOT NULL DEFAULT 'docker',
    external_id text
);

CREATE TABLE containers (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    container_host_id uuid NOT NULL REFERENCES container_hosts(entity_id) ON DELETE CASCADE,
    external_id text NOT NULL,
    image text NOT NULL,
    state text NOT NULL DEFAULT 'unknown',
    created_at_external timestamptz,
    started_at timestamptz,
    UNIQUE (container_host_id, external_id)
);

CREATE TABLE container_networks (
    entity_id uuid PRIMARY KEY REFERENCES entities(id) ON DELETE CASCADE,
    container_host_id uuid NOT NULL REFERENCES container_hosts(entity_id) ON DELETE CASCADE,
    external_id text NOT NULL,
    driver text,
    scope text,
    UNIQUE (container_host_id, external_id)
);

CREATE TABLE container_network_memberships (
    container_id uuid NOT NULL REFERENCES containers(entity_id) ON DELETE CASCADE,
    network_id uuid NOT NULL REFERENCES container_networks(entity_id) ON DELETE CASCADE,
    ip_address inet,
    mac_address macaddr,
    PRIMARY KEY (container_id, network_id)
);

CREATE TABLE relationships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    target_entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    relationship_type text NOT NULL CHECK (relationship_type IN (
        'CONNECTED_TO', 'HOSTS', 'RUNS_ON', 'MEMBER_OF', 'EXPOSES',
        'DEPENDS_ON', 'USES', 'COMMUNICATES_WITH', 'ROUTED_THROUGH'
    )),
    source text NOT NULL,
    confidence smallint NOT NULL CHECK (confidence BETWEEN 0 AND 100),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    manually_confirmed_at timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    CHECK (source_entity_id <> target_entity_id),
    UNIQUE (source_entity_id, target_entity_id, relationship_type, source)
);
CREATE INDEX relationships_source_idx ON relationships(source_entity_id, relationship_type);
CREATE INDEX relationships_target_idx ON relationships(target_entity_id, relationship_type);

CREATE TABLE observations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id uuid REFERENCES entities(id) ON DELETE CASCADE,
    source text NOT NULL,
    observation_type text NOT NULL,
    confidence smallint NOT NULL CHECK (confidence BETWEEN 0 AND 100),
    evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
    observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX observations_entity_time_idx ON observations(entity_id, observed_at DESC);

CREATE TABLE events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id uuid REFERENCES entities(id) ON DELETE SET NULL,
    type text NOT NULL,
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    category text NOT NULL,
    message text NOT NULL,
    details jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX events_time_idx ON events(occurred_at DESC);
CREATE INDEX events_entity_time_idx ON events(entity_id, occurred_at DESC);
CREATE INDEX events_category_severity_idx ON events(category, severity, occurred_at DESC);

CREATE TABLE metrics (
    entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    metric text NOT NULL,
    value_double double precision NOT NULL,
    unit text NOT NULL,
    observed_at timestamptz NOT NULL,
    source text NOT NULL,
    PRIMARY KEY (entity_id, metric, observed_at, source)
);
CREATE INDEX metrics_time_idx ON metrics(observed_at DESC);

CREATE TABLE integrations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL,
    name text NOT NULL,
    endpoint text,
    enabled boolean NOT NULL DEFAULT true,
    status text NOT NULL DEFAULT 'unknown',
    capabilities text[] NOT NULL DEFAULT '{}',
    adapter_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_sync_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE integration_credentials (
    integration_id uuid PRIMARY KEY REFERENCES integrations(id) ON DELETE CASCADE,
    ciphertext bytea NOT NULL,
    nonce bytea NOT NULL,
    key_version integer NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE actions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    entity_id uuid NOT NULL REFERENCES entities(id) ON DELETE RESTRICT,
    requested_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    action_type text NOT NULL,
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
    status text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);
CREATE INDEX actions_entity_time_idx ON actions(entity_id, requested_at DESC);

CREATE TABLE action_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    action_id uuid NOT NULL REFERENCES actions(id) ON DELETE RESTRICT,
    attempt integer NOT NULL CHECK (attempt > 0),
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    result text,
    error_code text,
    error_message text,
    UNIQUE (action_id, attempt)
);

CREATE TABLE audit_log (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text,
    safe_parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
    result text NOT NULL,
    source_address inet,
    duration_ms integer,
    error_code text,
    occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_time_idx ON audit_log(occurred_at DESC);
CREATE INDEX audit_log_actor_time_idx ON audit_log(actor_id, occurred_at DESC);

CREATE FUNCTION prevent_audit_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit records are immutable';
END;
$$;

CREATE TRIGGER audit_log_immutable
BEFORE UPDATE OR DELETE ON audit_log
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();

CREATE FUNCTION notify_nerve_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify(
        'nerve_events',
        json_build_object(
            'id', NEW.id,
            'entityId', NEW.entity_id,
            'type', NEW.type,
            'severity', NEW.severity,
            'category', NEW.category,
            'message', NEW.message,
            'occurredAt', NEW.occurred_at
        )::text
    );
    RETURN NEW;
END;
$$;

CREATE TRIGGER events_realtime_notify
AFTER INSERT ON events
FOR EACH ROW EXECUTE FUNCTION notify_nerve_event();


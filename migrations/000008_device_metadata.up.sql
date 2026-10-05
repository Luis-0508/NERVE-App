-- Device Identity, part 1: human-owned metadata, trust state and provenance.
--
-- Discovered facts (written by discovery and agents) and human-owned metadata
-- (written only by operators) live in separate columns. The effective value is
-- resolved by Core: manual override first, then the best discovered value.

ALTER TABLE devices
    ADD COLUMN display_name text CHECK (display_name IS NULL OR length(btrim(display_name)) BETWEEN 1 AND 200),
    ADD COLUMN discovered_name text,
    ADD COLUMN classification_override text CHECK (classification_override IS NULL OR classification_override IN (
        'workstation', 'laptop', 'server', 'virtual_machine', 'router', 'switch', 'access_point',
        'firewall', 'network', 'printer', 'phone', 'tablet', 'iot', 'camera', 'storage', 'media',
        'hypervisor', 'container_host', 'other'
    )),
    ADD COLUMN trust_state text NOT NULL DEFAULT 'unknown'
        CHECK (trust_state IN ('unknown', 'reviewed', 'trusted', 'blocked')),
    ADD COLUMN trust_changed_at timestamptz,
    ADD COLUMN trust_changed_by uuid REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN metadata_updated_at timestamptz,
    ADD COLUMN metadata_updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
    -- Set only by an operator merge: the retired record points at the survivor.
    ADD COLUMN merged_into uuid REFERENCES devices(entity_id) ON DELETE RESTRICT,
    ADD COLUMN merged_at timestamptz,
    ADD CONSTRAINT devices_merge_consistency CHECK ((merged_into IS NULL) = (merged_at IS NULL) AND merged_into IS DISTINCT FROM entity_id),
    ADD CONSTRAINT devices_notes_length CHECK (notes IS NULL OR length(notes) <= 10000),
    ADD CONSTRAINT devices_owner_length CHECK (owner IS NULL OR length(owner) <= 200),
    ADD CONSTRAINT devices_location_length CHECK (location IS NULL OR length(location) <= 200);

-- Until now discovery wrote the displayed name straight into entities.name.
-- That value becomes the discovered name; no device has a manual name yet.
UPDATE devices device SET discovered_name = entity.name
FROM entities entity WHERE entity.id = device.entity_id;

CREATE INDEX devices_trust_state_idx ON devices(trust_state);
CREATE INDEX devices_display_name_lower_idx ON devices(lower(display_name)) WHERE display_name IS NOT NULL;

-- Human-owned fields can only change together with the operator stamp that
-- the API writes. Discovery and agents never set the stamps, so PostgreSQL
-- itself rejects any attempt by them to overwrite operator decisions.
CREATE FUNCTION guard_device_owned_fields() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.trust_state IS DISTINCT FROM OLD.trust_state
       AND (NEW.trust_changed_by IS NULL OR NEW.trust_changed_at IS NOT DISTINCT FROM OLD.trust_changed_at) THEN
        RAISE EXCEPTION 'device trust state can only be changed by an identified operator';
    END IF;
    IF (NEW.display_name, NEW.classification_override, NEW.notes, NEW.owner, NEW.location)
           IS DISTINCT FROM (OLD.display_name, OLD.classification_override, OLD.notes, OLD.owner, OLD.location)
       AND (NEW.metadata_updated_by IS NULL OR NEW.metadata_updated_at IS NOT DISTINCT FROM OLD.metadata_updated_at) THEN
        RAISE EXCEPTION 'human-owned device metadata can only be changed by an identified operator';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER devices_owned_fields_guard
BEFORE UPDATE ON devices
FOR EACH ROW EXECUTE FUNCTION guard_device_owned_fields();

-- entities.name stays the effective display name, so every existing reader
-- (relationships, agents, map, events) shows the operator's name. A manual
-- name always wins over whatever discovery tries to write.
CREATE FUNCTION enforce_device_display_name() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    manual text;
BEGIN
    IF NEW.kind = 'device' THEN
        SELECT display_name INTO manual FROM devices WHERE entity_id = NEW.id;
        IF manual IS NOT NULL THEN
            NEW.name := manual;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER entities_device_display_name
BEFORE UPDATE OF name ON entities
FOR EACH ROW EXECUTE FUNCTION enforce_device_display_name();

CREATE FUNCTION sync_device_entity_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE entities SET name = COALESCE(NEW.display_name, NEW.discovered_name, NEW.hostname, name), updated_at = now()
    WHERE id = NEW.entity_id AND name IS DISTINCT FROM COALESCE(NEW.display_name, NEW.discovered_name, NEW.hostname, name);
    RETURN NEW;
END;
$$;

CREATE TRIGGER devices_entity_name_sync
AFTER UPDATE OF display_name, discovered_name ON devices
FOR EACH ROW EXECUTE FUNCTION sync_device_entity_name();

-- Operator tags: short, lower-case labels.
CREATE TABLE device_tags (
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    tag text NOT NULL CHECK (tag ~ '^[a-z0-9][a-z0-9._:-]{0,47}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    PRIMARY KEY (device_id, tag)
);
CREATE INDEX device_tags_tag_idx ON device_tags(tag);

-- Provenance of descriptive facts: one row per attribute and source. The
-- discovered value shown on the device is the most confident current claim.
CREATE TABLE device_attributes (
    device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    attribute text NOT NULL CHECK (attribute IN (
        'hostname', 'operating_system', 'os_version', 'architecture', 'manufacturer', 'model', 'serial_number'
    )),
    source text NOT NULL CHECK (source ~ '^[a-z][a-z0-9-]{1,40}$'),
    value text NOT NULL CHECK (length(value) BETWEEN 1 AND 256),
    confidence smallint NOT NULL CHECK (confidence BETWEEN 0 AND 100),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, attribute, source),
    CHECK (last_seen_at >= first_seen_at)
);

-- Existing discovered hostnames came from reverse DNS during local discovery.
INSERT INTO device_attributes (device_id, attribute, source, value, confidence, first_seen_at, last_seen_at)
SELECT entity_id, 'hostname', 'reverse-dns', left(hostname, 256), 50, now(), now()
FROM devices WHERE hostname IS NOT NULL AND btrim(hostname) <> ''
ON CONFLICT DO NOTHING;

-- Identity evidence gains IP addresses (weak) and agent machine fingerprints.
ALTER TABLE device_identities DROP CONSTRAINT device_identities_identity_type_check;
ALTER TABLE device_identities ADD CONSTRAINT device_identities_identity_type_check
    CHECK (identity_type IN ('mac', 'hostname', 'serial', 'agent', 'snmp', 'hypervisor', 'docker', 'manual', 'ip', 'machine'));

-- Permissions for operator-owned identity data.
INSERT INTO permissions (key, description) VALUES
    ('devices.manage', 'Edit device names, notes, tags, owner, location and classification'),
    ('devices.trust', 'Change the operator-owned trust state of devices'),
    ('devices.merge', 'Merge and split devices');

INSERT INTO role_permissions (role_id, permission_key)
SELECT role.id, permission.key
FROM roles role
JOIN permissions permission ON permission.key IN ('devices.manage', 'devices.trust', 'devices.merge')
WHERE role.key = 'administrator'
   OR (role.key = 'technician' AND permission.key = 'devices.manage');

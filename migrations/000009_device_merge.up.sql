-- Device Identity, part 3: operator-controlled merge and split.
--
-- A merge never deletes the retired device: its row stays as a tombstone with
-- merged_into pointing at the survivor, and every child record moves. Merge
-- and split records are append-only like the audit log.

CREATE TABLE device_merges (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_device_id uuid NOT NULL UNIQUE REFERENCES devices(entity_id) ON DELETE RESTRICT,
    target_device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE RESTRICT,
    merged_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    merged_at timestamptz NOT NULL DEFAULT now(),
    reason text CHECK (reason IS NULL OR length(reason) <= 500),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    CHECK (source_device_id <> target_device_id)
);
CREATE INDEX device_merges_target_idx ON device_merges(target_device_id, merged_at DESC);

CREATE TABLE device_splits (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_device_id uuid NOT NULL REFERENCES devices(entity_id) ON DELETE RESTRICT,
    new_device_id uuid NOT NULL UNIQUE REFERENCES devices(entity_id) ON DELETE RESTRICT,
    split_by uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    split_at timestamptz NOT NULL DEFAULT now(),
    reason text CHECK (reason IS NULL OR length(reason) <= 500),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    CHECK (source_device_id <> new_device_id)
);
CREATE INDEX device_splits_source_idx ON device_splits(source_device_id, split_at DESC);

CREATE TRIGGER device_merges_immutable BEFORE UPDATE OR DELETE ON device_merges
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();
CREATE TRIGGER device_splits_immutable BEFORE UPDATE OR DELETE ON device_splits
FOR EACH ROW EXECUTE FUNCTION prevent_audit_mutation();

-- Operators can declare that two records are different devices, so the pair
-- stops appearing as a duplicate candidate. Stored as an ordered pair.
CREATE TABLE device_duplicate_dismissals (
    device_a uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    device_b uuid NOT NULL REFERENCES devices(entity_id) ON DELETE CASCADE,
    dismissed_by uuid REFERENCES users(id) ON DELETE SET NULL,
    dismissed_at timestamptz NOT NULL DEFAULT now(),
    reason text CHECK (reason IS NULL OR length(reason) <= 500),
    PRIMARY KEY (device_a, device_b),
    CHECK (device_a < device_b)
);

-- Nothing may be attached to a retired device. This closes the race where
-- discovery or an agent writes to a device while an operator merges it away.
CREATE FUNCTION reject_merged_device_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    referenced uuid := (to_jsonb(NEW) ->> TG_ARGV[0])::uuid;
BEGIN
    IF referenced IS NOT NULL AND EXISTS (SELECT 1 FROM devices WHERE entity_id = referenced AND merged_into IS NOT NULL) THEN
        RAISE EXCEPTION 'device % was merged into another device', referenced USING ERRCODE = 'P0001', HINT = 'device_merged';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER network_interfaces_not_merged BEFORE INSERT OR UPDATE OF device_id ON network_interfaces
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');
CREATE TRIGGER device_identities_not_merged BEFORE INSERT OR UPDATE OF device_id ON device_identities
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');
CREATE TRIGGER device_attributes_not_merged BEFORE INSERT OR UPDATE OF device_id ON device_attributes
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');
CREATE TRIGGER device_tags_not_merged BEFORE INSERT OR UPDATE OF device_id ON device_tags
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');
CREATE TRIGGER discovery_bindings_not_merged BEFORE INSERT OR UPDATE OF device_id ON discovery_bindings
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');
CREATE TRIGGER agents_not_merged BEFORE INSERT OR UPDATE OF device_id ON agents
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('device_id');
CREATE TRIGGER service_endpoints_not_merged BEFORE INSERT OR UPDATE OF host_entity_id ON service_endpoints
FOR EACH ROW EXECUTE FUNCTION reject_merged_device_reference('host_entity_id');

-- A retired device stays retired and always points at a live device.
CREATE FUNCTION guard_device_merge_state() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.merged_into IS NOT NULL AND NEW.merged_into IS NULL THEN
        RAISE EXCEPTION 'a merged device cannot be restored';
    END IF;
    IF NEW.merged_into IS NOT NULL AND EXISTS (SELECT 1 FROM devices WHERE entity_id = NEW.merged_into AND merged_into IS NOT NULL) THEN
        RAISE EXCEPTION 'devices can only be merged into a live device';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER devices_merge_state_guard BEFORE UPDATE OF merged_into ON devices
FOR EACH ROW EXECUTE FUNCTION guard_device_merge_state();

CREATE INDEX devices_merged_into_idx ON devices(merged_into) WHERE merged_into IS NOT NULL;

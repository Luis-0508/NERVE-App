DELETE FROM role_permissions WHERE permission_key IN ('devices.manage', 'devices.trust', 'devices.merge');
DELETE FROM permissions WHERE key IN ('devices.manage', 'devices.trust', 'devices.merge');
DELETE FROM device_identities WHERE identity_type IN ('ip', 'machine');
ALTER TABLE device_identities DROP CONSTRAINT device_identities_identity_type_check;
ALTER TABLE device_identities ADD CONSTRAINT device_identities_identity_type_check
    CHECK (identity_type IN ('mac', 'hostname', 'serial', 'agent', 'snmp', 'hypervisor', 'docker', 'manual'));
DROP TABLE IF EXISTS device_attributes;
DROP TABLE IF EXISTS device_tags;
DROP TRIGGER IF EXISTS devices_entity_name_sync ON devices;
DROP FUNCTION IF EXISTS sync_device_entity_name();
DROP TRIGGER IF EXISTS entities_device_display_name ON entities;
DROP FUNCTION IF EXISTS enforce_device_display_name();
DROP TRIGGER IF EXISTS devices_owned_fields_guard ON devices;
DROP FUNCTION IF EXISTS guard_device_owned_fields();
ALTER TABLE devices
    DROP CONSTRAINT IF EXISTS devices_notes_length,
    DROP CONSTRAINT IF EXISTS devices_owner_length,
    DROP CONSTRAINT IF EXISTS devices_location_length,
    DROP CONSTRAINT IF EXISTS devices_merge_consistency,
    DROP COLUMN IF EXISTS merged_at,
    DROP COLUMN IF EXISTS merged_into,
    DROP COLUMN IF EXISTS metadata_updated_by,
    DROP COLUMN IF EXISTS metadata_updated_at,
    DROP COLUMN IF EXISTS trust_changed_by,
    DROP COLUMN IF EXISTS trust_changed_at,
    DROP COLUMN IF EXISTS trust_state,
    DROP COLUMN IF EXISTS classification_override,
    DROP COLUMN IF EXISTS discovered_name,
    DROP COLUMN IF EXISTS display_name;

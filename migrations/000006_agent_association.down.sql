DROP INDEX IF EXISTS device_identities_device_type_idx;
DROP INDEX IF EXISTS agents_reported_mac_idx;
UPDATE agents SET association_status = 'unassociated' WHERE association_status = 'dismissed';
ALTER TABLE agents DROP CONSTRAINT agents_association_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_association_status_check
    CHECK (association_status IN ('unassociated', 'candidate', 'automatic', 'confirmed'));

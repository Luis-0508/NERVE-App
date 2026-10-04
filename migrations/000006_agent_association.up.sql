-- 'dismissed' records an operator decision that an agent belongs to no known
-- device. Automatic evaluation never overrides it (nor 'confirmed').
ALTER TABLE agents DROP CONSTRAINT agents_association_status_check;
ALTER TABLE agents ADD CONSTRAINT agents_association_status_check
    CHECK (association_status IN ('unassociated', 'candidate', 'automatic', 'confirmed', 'dismissed'));

-- Candidate lookup by reported evidence.
CREATE INDEX agents_reported_mac_idx ON agents USING gin (reported_mac_addresses) WHERE status <> 'removed';
CREATE INDEX device_identities_device_type_idx ON device_identities(device_id, identity_type);

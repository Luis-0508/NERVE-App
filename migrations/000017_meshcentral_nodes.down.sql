DROP TABLE meshcentral_sync_state;
DROP TABLE meshcentral_nodes;
DELETE FROM device_identities WHERE identity_type = 'meshcentral';
ALTER TABLE device_identities DROP CONSTRAINT device_identities_identity_type_check;
ALTER TABLE device_identities ADD CONSTRAINT device_identities_identity_type_check
    CHECK (identity_type IN ('mac', 'hostname', 'serial', 'agent', 'snmp', 'hypervisor', 'docker', 'manual', 'ip', 'machine'));

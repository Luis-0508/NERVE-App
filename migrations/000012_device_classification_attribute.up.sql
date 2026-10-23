-- Rule-based classification suggestions are stored as sourced claims like
-- other discovered facts (source 'heuristic'); devices.classification follows
-- the most confident claim and classification_override still wins.
-- Numbered 000012: 000011 is reserved for agent deployment keys (K-22).
ALTER TABLE device_attributes DROP CONSTRAINT device_attributes_attribute_check;
ALTER TABLE device_attributes ADD CONSTRAINT device_attributes_attribute_check CHECK (attribute IN (
    'hostname', 'operating_system', 'os_version', 'architecture', 'manufacturer', 'model', 'serial_number',
    'classification'
));

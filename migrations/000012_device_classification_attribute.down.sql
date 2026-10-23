DELETE FROM device_attributes WHERE attribute = 'classification';
UPDATE devices SET classification = 'unknown';
ALTER TABLE device_attributes DROP CONSTRAINT device_attributes_attribute_check;
ALTER TABLE device_attributes ADD CONSTRAINT device_attributes_attribute_check CHECK (attribute IN (
    'hostname', 'operating_system', 'os_version', 'architecture', 'manufacturer', 'model', 'serial_number'
));

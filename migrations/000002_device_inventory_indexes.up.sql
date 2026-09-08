CREATE INDEX devices_classification_idx ON devices(classification);
CREATE INDEX devices_hostname_lower_idx ON devices(lower(hostname)) WHERE hostname IS NOT NULL;
CREATE INDEX entities_device_name_lower_idx ON entities(lower(name)) WHERE kind = 'device';

-- Guarantee the global discovery invariant in PostgreSQL itself so concurrent
-- API requests cannot queue more than one active scan.
CREATE UNIQUE INDEX discovery_scans_single_active_idx
ON discovery_scans ((1))
WHERE status IN ('queued', 'running');

-- Milestone 3 discovery cannot reliably distinguish remote Ethernet and Wi-Fi
-- interfaces from ARP/NDP evidence alone. Remove the old guessed media type so
-- topology does not render invented link information.
UPDATE network_interfaces interface
SET media_type = NULL
FROM entities entity
WHERE entity.id = interface.entity_id
  AND entity.metadata ->> 'managedBy' = 'local-discovery'
  AND interface.media_type = 'ethernet';

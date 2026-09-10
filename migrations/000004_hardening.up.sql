-- Repair any duplicate active jobs that could have been created by the old
-- optimistic application-side check before enforcing the invariant below.
WITH ranked_active AS (
    SELECT id,
           row_number() OVER (ORDER BY requested_at, id) AS position
    FROM discovery_scans
    WHERE status IN ('queued', 'running')
)
UPDATE discovery_scans scan
SET status = 'failed',
    completed_at = COALESCE(scan.completed_at, now()),
    error_message = COALESCE(scan.error_message, 'Superseded while enforcing the single-active-scan invariant.')
FROM ranked_active ranked
WHERE scan.id = ranked.id
  AND ranked.position > 1;

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

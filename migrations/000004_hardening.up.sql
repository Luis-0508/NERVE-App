-- Repair any duplicate active jobs that could have been created by the old
-- optimistic application-side check before enforcing the invariant below.
WITH ranked_active AS (
    SELECT id,
           row_number() OVER (
               ORDER BY CASE status WHEN 'running' THEN 0 ELSE 1 END, requested_at, id
           ) AS position
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

-- Enforce child lifecycle consistency at successful scan completion. A device
-- may remain online while one of its old addresses or interfaces disappears, so
-- stale child state cannot be derived only from the device's overall status.
CREATE FUNCTION reconcile_discovery_scan_children() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.status = 'succeeded' AND OLD.status = 'running' AND NEW.started_at IS NOT NULL THEN
        UPDATE ip_addresses ip
        SET valid_until = COALESCE(ip.valid_until, now())
        FROM discovery_bindings binding
        WHERE binding.target_id = NEW.target_id
          AND binding.last_seen_at < NEW.started_at
          AND binding.ip_address_id = ip.entity_id
          AND ip.valid_until IS NULL;

        UPDATE entities ip_entity
        SET status = 'offline', updated_at = now()
        FROM discovery_bindings binding
        WHERE binding.target_id = NEW.target_id
          AND binding.last_seen_at < NEW.started_at
          AND binding.ip_address_id = ip_entity.id;

        UPDATE network_interfaces interface
        SET is_up = false
        WHERE EXISTS (
            SELECT 1 FROM discovery_bindings binding
            WHERE binding.target_id = NEW.target_id
              AND binding.interface_id = interface.entity_id
        )
        AND NOT EXISTS (
            SELECT 1 FROM discovery_bindings binding
            WHERE binding.target_id = NEW.target_id
              AND binding.interface_id = interface.entity_id
              AND binding.last_seen_at >= NEW.started_at
        );

        UPDATE entities interface_entity
        SET status = 'offline', updated_at = now()
        FROM network_interfaces interface
        WHERE interface_entity.id = interface.entity_id
          AND EXISTS (
              SELECT 1 FROM discovery_bindings binding
              WHERE binding.target_id = NEW.target_id
                AND binding.interface_id = interface.entity_id
          )
          AND NOT EXISTS (
              SELECT 1 FROM discovery_bindings binding
              WHERE binding.target_id = NEW.target_id
                AND binding.interface_id = interface.entity_id
                AND binding.last_seen_at >= NEW.started_at
          );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER discovery_scan_children_reconcile
AFTER UPDATE OF status ON discovery_scans
FOR EACH ROW EXECUTE FUNCTION reconcile_discovery_scan_children();

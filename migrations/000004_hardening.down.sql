DROP TRIGGER IF EXISTS discovery_scan_children_reconcile ON discovery_scans;
DROP FUNCTION IF EXISTS reconcile_discovery_scan_children();
DROP INDEX IF EXISTS discovery_scans_single_active_idx;

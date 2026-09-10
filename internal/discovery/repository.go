package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNoJob       = errors.New("no discovery job is queued")
	ErrScanActive  = errors.New("a discovery scan is already queued or running")
	ErrInvalidCIDR = errors.New("subnet must be a canonical IPv4 or IPv6 CIDR containing at most 4096 addresses")
	ErrInvalidJob  = errors.New("discovery job is not running for this agent")
)

type Repository struct{ db *pgxpool.Pool }

func NewRepository(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func NormalizeCIDR(value string) (string, int, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || prefix != prefix.Masked() {
		return "", 0, ErrInvalidCIDR
	}
	hostBits := prefix.Addr().BitLen() - prefix.Bits()
	if hostBits < 0 || hostBits > 12 {
		return "", 0, ErrInvalidCIDR
	}
	total := 1 << hostBits
	if prefix.Addr().Is4() && prefix.Bits() <= 30 {
		total -= 2
	}
	return prefix.String(), total, nil
}

func (r *Repository) SaveTarget(ctx context.Context, name, cidr, userID string) (Target, error) {
	normalized, _, err := NormalizeCIDR(cidr)
	if err != nil {
		return Target{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = normalized
	}
	var target Target
	err = r.db.QueryRow(ctx, `
		INSERT INTO discovery_targets (name, cidr, created_by)
		VALUES ($1, $2::cidr, $3::uuid)
		ON CONFLICT (cidr) DO UPDATE SET name = EXCLUDED.name, enabled = true, updated_at = now()
		RETURNING id::text, name, cidr::text, enabled, created_at, updated_at`, name, normalized, userID).Scan(
		&target.ID, &target.Name, &target.CIDR, &target.Enabled, &target.CreatedAt, &target.UpdatedAt,
	)
	if err != nil {
		return Target{}, fmt.Errorf("save discovery target: %w", err)
	}
	return target, nil
}

func (r *Repository) State(ctx context.Context) (State, error) {
	var state State
	var target Target
	err := r.db.QueryRow(ctx, `SELECT id::text, name, cidr::text, enabled, created_at, updated_at
		FROM discovery_targets ORDER BY updated_at DESC LIMIT 1`).Scan(
		&target.ID, &target.Name, &target.CIDR, &target.Enabled, &target.CreatedAt, &target.UpdatedAt,
	)
	if err == nil {
		state.Target = &target
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return State{}, fmt.Errorf("query discovery target: %w", err)
	}

	var agent AgentStatus
	err = r.db.QueryRow(ctx, `SELECT id, hostname, platform, version, capabilities,
		last_heartbeat_at, COALESCE(last_error, ''), last_heartbeat_at > now() - interval '30 seconds'
		FROM discovery_agents ORDER BY last_heartbeat_at DESC LIMIT 1`).Scan(
		&agent.ID, &agent.Hostname, &agent.Platform, &agent.Version, &agent.Capabilities,
		&agent.LastHeartbeatAt, &agent.LastError, &agent.Online,
	)
	if err == nil {
		state.Agent = &agent
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return State{}, fmt.Errorf("query discovery agent: %w", err)
	}

	if state.Target != nil {
		scan, scanErr := r.latestScan(ctx, state.Target.ID)
		if scanErr == nil {
			state.Scan = &scan
		} else if !errors.Is(scanErr, pgx.ErrNoRows) {
			return State{}, scanErr
		}
	}
	return state, nil
}

func (r *Repository) Queue(ctx context.Context, targetID, userID string) (Scan, error) {
	var active bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM discovery_scans WHERE status IN ('queued', 'running'))`).Scan(&active); err != nil {
		return Scan{}, fmt.Errorf("check active discovery scans: %w", err)
	}
	if active {
		return Scan{}, ErrScanActive
	}
	var scan Scan
	err := r.db.QueryRow(ctx, `
		INSERT INTO discovery_scans (target_id, requested_by)
		SELECT id, $2::uuid FROM discovery_targets WHERE id = $1::uuid AND enabled
		RETURNING id::text, target_id::text, status, requested_at, addresses_total, addresses_reachable, error_count`, targetID, userID).Scan(
		&scan.ID, &scan.TargetID, &scan.Status, &scan.RequestedAt, &scan.AddressesTotal, &scan.AddressesReachable, &scan.ErrorCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scan{}, ErrInvalidCIDR
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "discovery_scans_single_active_idx" {
			return Scan{}, ErrScanActive
		}
		return Scan{}, fmt.Errorf("queue discovery scan: %w", err)
	}
	if err := r.db.QueryRow(ctx, `SELECT cidr::text FROM discovery_targets WHERE id = $1::uuid`, targetID).Scan(&scan.TargetCIDR); err != nil {
		return Scan{}, fmt.Errorf("read queued discovery target: %w", err)
	}
	scan.Errors = []ScanError{}
	return scan, nil
}

func (r *Repository) Heartbeat(ctx context.Context, input AgentHeartbeat) error {
	_, err := r.db.Exec(ctx, `INSERT INTO discovery_agents (id, hostname, platform, version, capabilities, last_error)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		ON CONFLICT (id) DO UPDATE SET hostname = EXCLUDED.hostname, platform = EXCLUDED.platform,
		version = EXCLUDED.version, capabilities = EXCLUDED.capabilities,
		last_heartbeat_at = now(), last_error = EXCLUDED.last_error, updated_at = now()`,
		input.ID, input.Hostname, input.Platform, input.Version, input.Capabilities, input.LastError)
	if err != nil {
		return fmt.Errorf("record discovery agent heartbeat: %w", err)
	}
	return nil
}

func (r *Repository) AgentReady(ctx context.Context) bool {
	var ready bool
	if err := r.db.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM discovery_agents WHERE last_heartbeat_at > now() - interval '30 seconds'
	)`).Scan(&ready); err != nil {
		return false
	}
	return ready
}

func (r *Repository) Claim(ctx context.Context, agentID string) (Job, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Job{}, fmt.Errorf("begin discovery claim: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE discovery_scans SET status = 'failed', completed_at = now(),
		error_message = 'Discovery agent stopped before completing the scan.'
		WHERE status = 'running' AND started_at < now() - interval '10 minutes'`); err != nil {
		return Job{}, fmt.Errorf("expire abandoned discovery jobs: %w", err)
	}
	var job Job
	err = tx.QueryRow(ctx, `WITH next_job AS (
		SELECT id FROM discovery_scans WHERE status = 'queued' ORDER BY requested_at FOR UPDATE SKIP LOCKED LIMIT 1
	)
	UPDATE discovery_scans scan SET status = 'running', agent_id = $1, started_at = now()
	FROM next_job, discovery_targets target
	WHERE scan.id = next_job.id AND target.id = scan.target_id
	RETURNING scan.id::text, target.cidr::text`, agentID).Scan(&job.ID, &job.CIDR)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNoJob
	}
	if err != nil {
		return Job{}, fmt.Errorf("claim discovery job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, fmt.Errorf("commit discovery claim: %w", err)
	}
	return job, nil
}

func (r *Repository) Report(ctx context.Context, scanID, agentID string, report Report) error {
	if len(report.Observations) > 4096 || len(report.Errors) > 1024 {
		return errors.New("discovery report exceeds the supported batch size")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin discovery report: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var targetID, cidr string
	if err := tx.QueryRow(ctx, `SELECT scan.target_id::text, target.cidr::text
		FROM discovery_scans scan JOIN discovery_targets target ON target.id = scan.target_id
		WHERE scan.id = $1::uuid AND scan.agent_id = $2 AND scan.status = 'running'
		FOR UPDATE`, scanID, agentID).Scan(&targetID, &cidr); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidJob
		}
		return fmt.Errorf("lock discovery report: %w", err)
	}
	prefix, _ := netip.ParsePrefix(cidr)
	reachable := 0
	for _, observation := range report.Observations {
		address, parseErr := netip.ParseAddr(strings.TrimSpace(observation.Address))
		if parseErr != nil || !prefix.Contains(address) || !observation.Reachable {
			continue
		}
		observation.Address = address.String()
		if err := r.applyObservation(ctx, tx, targetID, scanID, observation); err != nil {
			return err
		}
		reachable++
	}
	for _, scanError := range report.Errors {
		_, err := tx.Exec(ctx, `INSERT INTO discovery_scan_errors (scan_id, address, stage, message)
			VALUES ($1::uuid, NULLIF($2, '')::inet, $3, $4)`, scanID, scanError.Address, truncate(scanError.Stage, 80), truncate(scanError.Message, 1000))
		if err != nil {
			return fmt.Errorf("record discovery scan error: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `UPDATE discovery_scans SET addresses_reachable = addresses_reachable + $2,
		error_count = error_count + $3 WHERE id = $1::uuid`, scanID, reachable, len(report.Errors))
	if err != nil {
		return fmt.Errorf("update discovery scan counters: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit discovery report: %w", err)
	}
	return nil
}

func (r *Repository) applyObservation(ctx context.Context, tx pgx.Tx, targetID, scanID string, observation Observation) error {
	hostname := strings.TrimSuffix(strings.TrimSpace(observation.Hostname), ".")
	mac := normalizeMAC(observation.MAC)
	var deviceID string
	if mac != "" {
		err := tx.QueryRow(ctx, `SELECT device_id::text FROM device_identities
			WHERE identity_type = 'mac' AND lower(value) = $1 ORDER BY confidence DESC, first_seen_at LIMIT 1`, mac).Scan(&deviceID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("resolve device MAC identity: %w", err)
		}
	}
	var boundDeviceID, boundInterfaceID, boundIPID string
	bindingErr := tx.QueryRow(ctx, `SELECT device_id::text, interface_id::text, ip_address_id::text
		FROM discovery_bindings WHERE target_id = $1::uuid AND address = $2::inet`, targetID, observation.Address).Scan(
		&boundDeviceID, &boundInterfaceID, &boundIPID,
	)
	if bindingErr != nil && !errors.Is(bindingErr, pgx.ErrNoRows) {
		return fmt.Errorf("resolve discovery address binding: %w", bindingErr)
	}
	if deviceID == "" && bindingErr == nil {
		deviceID = boundDeviceID
	}
	created := false
	if deviceID == "" {
		name := hostname
		if name == "" {
			name = observation.Address
		}
		if err := tx.QueryRow(ctx, `INSERT INTO entities (kind, name, status, metadata)
			VALUES ('device', $1, 'online', '{"managedBy":"local-discovery"}'::jsonb) RETURNING id::text`, name).Scan(&deviceID); err != nil {
			return fmt.Errorf("create discovered device entity: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO devices (entity_id, classification, hostname)
			VALUES ($1::uuid, 'unknown', NULLIF($2, ''))`, deviceID, hostname); err != nil {
			return fmt.Errorf("create discovered device: %w", err)
		}
		created = true
	}
	if _, err := tx.Exec(ctx, `UPDATE entities SET name = CASE WHEN $2 <> '' THEN $2 ELSE name END,
		status = 'online', last_seen_at = now(), updated_at = now() WHERE id = $1::uuid`, deviceID, hostname); err != nil {
		return fmt.Errorf("update discovered device: %w", err)
	}
	if hostname != "" {
		if _, err := tx.Exec(ctx, `UPDATE devices SET hostname = $2 WHERE entity_id = $1::uuid`, deviceID, hostname); err != nil {
			return fmt.Errorf("update discovered hostname: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence)
			VALUES ($1::uuid, 'hostname', lower($2), 'local-discovery', 65)
			ON CONFLICT (identity_type, value, device_id) DO UPDATE SET last_seen_at = now(), confidence = GREATEST(device_identities.confidence, 65)`, deviceID, hostname); err != nil {
			return fmt.Errorf("record hostname identity: %w", err)
		}
	}
	if mac != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence)
			VALUES ($1::uuid, 'mac', $2, 'local-discovery', 90)
			ON CONFLICT (identity_type, value, device_id) DO UPDATE SET last_seen_at = now(), confidence = GREATEST(device_identities.confidence, 90)`, deviceID, mac); err != nil {
			return fmt.Errorf("record MAC identity: %w", err)
		}
	}

	interfaceID := ""
	if bindingErr == nil && boundDeviceID == deviceID {
		interfaceID = boundInterfaceID
	}
	if interfaceID == "" && mac != "" {
		err := tx.QueryRow(ctx, `SELECT entity_id::text FROM network_interfaces
			WHERE device_id = $1::uuid AND mac_address = $2::macaddr LIMIT 1`, deviceID, mac).Scan(&interfaceID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("resolve discovered interface: %w", err)
		}
	}
	if interfaceID == "" {
		interfaceName := "discovered"
		if mac != "" {
			interfaceName = "mac-" + strings.ReplaceAll(mac, ":", "")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO entities (kind, name, status, metadata)
			VALUES ('network_interface', $1, 'online', '{"managedBy":"local-discovery"}'::jsonb) RETURNING id::text`, interfaceName).Scan(&interfaceID); err != nil {
			return fmt.Errorf("create discovered interface entity: %w", err)
		}
		var macValue any
		if mac != "" {
			macValue = mac
		}
		if _, err := tx.Exec(ctx, `INSERT INTO network_interfaces (entity_id, device_id, name, mac_address, media_type, is_up)
			VALUES ($1::uuid, $2::uuid, $3, $4::macaddr, NULL, true)`, interfaceID, deviceID, interfaceName, macValue); err != nil {
			return fmt.Errorf("create discovered interface: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE entities SET status = 'online', last_seen_at = now(), updated_at = now() WHERE id = $1::uuid`, interfaceID); err != nil {
			return fmt.Errorf("update discovered interface entity: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE network_interfaces SET mac_address = COALESCE(NULLIF($2::text, '')::macaddr, mac_address), is_up = true WHERE entity_id = $1::uuid`, interfaceID, mac); err != nil {
			return fmt.Errorf("update discovered interface: %w", err)
		}
	}

	ipID := ""
	if bindingErr == nil && boundDeviceID == deviceID {
		ipID = boundIPID
	}
	if ipID == "" {
		if err := tx.QueryRow(ctx, `INSERT INTO entities (kind, name, status, metadata)
			VALUES ('ip_address', $1, 'online', '{"managedBy":"local-discovery"}'::jsonb) RETURNING id::text`, observation.Address).Scan(&ipID); err != nil {
			return fmt.Errorf("create discovered IP entity: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ip_addresses (entity_id, address, is_primary) VALUES ($1::uuid, $2::inet, true)`, ipID, observation.Address); err != nil {
			return fmt.Errorf("create discovered IP address: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO interface_addresses (interface_id, ip_address_id) VALUES ($1::uuid, $2::uuid)`, interfaceID, ipID); err != nil {
			return fmt.Errorf("attach discovered IP address: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE entities SET status = 'online', last_seen_at = now(), updated_at = now() WHERE id = $1::uuid`, ipID); err != nil {
		return fmt.Errorf("update discovered IP entity: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ip_addresses SET valid_until = NULL WHERE entity_id = $1::uuid`, ipID); err != nil {
		return fmt.Errorf("reactivate discovered IP address: %w", err)
	}
	if bindingErr == nil && boundDeviceID != deviceID {
		_, _ = tx.Exec(ctx, `UPDATE ip_addresses SET valid_until = now() WHERE entity_id = $1::uuid AND valid_until IS NULL`, boundIPID)
		_, _ = tx.Exec(ctx, `UPDATE entities SET status = 'offline', updated_at = now() WHERE id = $1::uuid`, boundIPID)
	}
	var macValue any
	if mac != "" {
		macValue = mac
	}
	if _, err := tx.Exec(ctx, `INSERT INTO discovery_bindings (target_id, address, device_id, interface_id, ip_address_id, mac_address)
		VALUES ($1::uuid, $2::inet, $3::uuid, $4::uuid, $5::uuid, $6::macaddr)
		ON CONFLICT (target_id, address) DO UPDATE SET device_id = EXCLUDED.device_id,
		interface_id = EXCLUDED.interface_id, ip_address_id = EXCLUDED.ip_address_id,
		mac_address = COALESCE(EXCLUDED.mac_address, discovery_bindings.mac_address), last_seen_at = now()`,
		targetID, observation.Address, deviceID, interfaceID, ipID, macValue); err != nil {
		return fmt.Errorf("save discovery address binding: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE interface_addresses SET last_seen_at = now() WHERE interface_id = $1::uuid AND ip_address_id = $2::uuid`, interfaceID, ipID); err != nil {
		return fmt.Errorf("update discovered address observation: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO observations (entity_id, source, observation_type, confidence, evidence)
		VALUES ($1::uuid, 'local-discovery', 'reachability', $2, jsonb_build_object('scanId', $3::text, 'address', $4::text, 'method', $5::text, 'hostname', NULLIF($6::text, ''), 'mac', NULLIF($7::text, '')))`,
		deviceID, observationConfidence(observation), scanID, observation.Address, observation.Method, hostname, mac); err != nil {
		return fmt.Errorf("record discovery observation: %w", err)
	}
	if created {
		if _, err := tx.Exec(ctx, `INSERT INTO events (entity_id, type, severity, category, message, details)
			VALUES ($1::uuid, 'device.discovered', 'info', 'discovery', 'A device was discovered on the local network.', jsonb_build_object('address', $2::text))`, deviceID, observation.Address); err != nil {
			return fmt.Errorf("record discovered device event: %w", err)
		}
	}
	return nil
}

func (r *Repository) Complete(ctx context.Context, scanID, agentID string, completion Completion) error {
	if completion.Status != "succeeded" && completion.Status != "partial" && completion.Status != "failed" {
		return errors.New("invalid discovery completion status")
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin discovery completion: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var targetID string
	var startedAt time.Time
	err = tx.QueryRow(ctx, `UPDATE discovery_scans SET status = $3, completed_at = now(), addresses_total = $4,
		error_message = NULLIF($5, '') WHERE id = $1::uuid AND agent_id = $2 AND status = 'running'
		RETURNING target_id::text, started_at`, scanID, agentID, completion.Status, completion.AddressesTotal, truncate(completion.ErrorMessage, 1000)).Scan(&targetID, &startedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidJob
	}
	if err != nil {
		return fmt.Errorf("complete discovery scan: %w", err)
	}
	if completion.Status == "succeeded" {
		if _, err := tx.Exec(ctx, `UPDATE entities entity SET status = 'offline', updated_at = now()
			WHERE entity.kind = 'device'
			AND EXISTS (SELECT 1 FROM discovery_bindings binding WHERE binding.target_id = $1::uuid AND binding.device_id = entity.id)
			AND NOT EXISTS (SELECT 1 FROM discovery_bindings binding WHERE binding.target_id = $1::uuid AND binding.device_id = entity.id AND binding.last_seen_at >= $2)`, targetID, startedAt); err != nil {
			return fmt.Errorf("mark unseen devices offline: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE network_interfaces interface SET is_up = false
			FROM entities device
			WHERE device.id = interface.device_id AND device.status = 'offline'
			AND EXISTS (SELECT 1 FROM discovery_bindings binding WHERE binding.target_id = $1::uuid AND binding.device_id = device.id)`, targetID); err != nil {
			return fmt.Errorf("mark unseen network interfaces down: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE entities interface_entity SET status = 'offline', updated_at = now()
			FROM network_interfaces interface, entities device
			WHERE interface_entity.id = interface.entity_id AND device.id = interface.device_id AND device.status = 'offline'
			AND EXISTS (SELECT 1 FROM discovery_bindings binding WHERE binding.target_id = $1::uuid AND binding.device_id = device.id)`, targetID); err != nil {
			return fmt.Errorf("mark unseen interface entities offline: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE entities ip_entity SET status = 'offline', updated_at = now()
			FROM ip_addresses ip, interface_addresses assignment, network_interfaces interface, entities device
			WHERE ip_entity.id = ip.entity_id AND assignment.ip_address_id = ip.entity_id
			AND interface.entity_id = assignment.interface_id AND device.id = interface.device_id
			AND device.status = 'offline' AND ip.valid_until IS NULL
			AND EXISTS (SELECT 1 FROM discovery_bindings binding WHERE binding.target_id = $1::uuid AND binding.device_id = device.id)`, targetID); err != nil {
			return fmt.Errorf("mark unseen IP entities offline: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (type, severity, category, message, details)
		VALUES ('discovery.scan.completed', CASE WHEN $2 = 'failed' THEN 'warning' ELSE 'info' END,
		'discovery', CASE WHEN $2 = 'failed' THEN 'Local network discovery failed.' ELSE 'Local network discovery completed.' END,
		jsonb_build_object('scanId', $1::text, 'status', $2::text))`, scanID, completion.Status); err != nil {
		return fmt.Errorf("record discovery completion event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit discovery completion: %w", err)
	}
	return nil
}

func (r *Repository) latestScan(ctx context.Context, targetID string) (Scan, error) {
	var scan Scan
	err := r.db.QueryRow(ctx, `SELECT scan.id::text, scan.target_id::text, target.cidr::text,
		COALESCE(scan.agent_id, ''), scan.status, scan.requested_at, scan.started_at, scan.completed_at,
		scan.addresses_total, scan.addresses_reachable, scan.error_count, COALESCE(scan.error_message, '')
		FROM discovery_scans scan JOIN discovery_targets target ON target.id = scan.target_id
		WHERE scan.target_id = $1::uuid ORDER BY scan.requested_at DESC LIMIT 1`, targetID).Scan(
		&scan.ID, &scan.TargetID, &scan.TargetCIDR, &scan.AgentID, &scan.Status, &scan.RequestedAt,
		&scan.StartedAt, &scan.CompletedAt, &scan.AddressesTotal, &scan.AddressesReachable, &scan.ErrorCount, &scan.ErrorMessage,
	)
	if err != nil {
		return Scan{}, err
	}
	rows, err := r.db.Query(ctx, `SELECT COALESCE(host(address), ''), stage, message, occurred_at
		FROM discovery_scan_errors WHERE scan_id = $1::uuid ORDER BY id DESC LIMIT 20`, scan.ID)
	if err != nil {
		return Scan{}, fmt.Errorf("query discovery errors: %w", err)
	}
	defer rows.Close()
	scan.Errors = []ScanError{}
	for rows.Next() {
		var item ScanError
		if err := rows.Scan(&item.Address, &item.Stage, &item.Message, &item.OccurredAt); err != nil {
			return Scan{}, fmt.Errorf("scan discovery error: %w", err)
		}
		scan.Errors = append(scan.Errors, item)
	}
	return scan, rows.Err()
}

func normalizeMAC(value string) string {
	hardware, err := net.ParseMAC(strings.TrimSpace(value))
	if err != nil || len(hardware) != 6 {
		return ""
	}
	return strings.ToLower(hardware.String())
}

func observationConfidence(observation Observation) int {
	if normalizeMAC(observation.MAC) != "" {
		return 90
	}
	if observation.Hostname != "" {
		return 70
	}
	return 55
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

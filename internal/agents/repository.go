package agents

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/audit"
	"github.com/nerve-labs/nerve/internal/devices"
)

type Repository struct {
	db      *pgxpool.Pool
	now     func() time.Time
	coreKey ed25519.PrivateKey
}

// SetCoreSigningKey configures the key whose fingerprint is embedded in new
// enrollment tokens and which signs agent-API responses.
func (r *Repository) SetCoreSigningKey(key ed25519.PrivateKey) { r.coreKey = key }

// CoreSigningKey returns the configured key, or nil.
func (r *Repository) CoreSigningKey() ed25519.PrivateKey { return r.coreKey }

func (r *Repository) coreFingerprint() string {
	if r.coreKey == nil {
		return ""
	}
	return agentproto.CoreFingerprint(r.coreKey.Public().(ed25519.PublicKey))
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db, now: time.Now}
}

// ---------------------------------------------------------------------------
// Enrollment tokens

// CreateEnrollment stores a new enrollment and returns the one-time token.
// Only the SHA-256 digest of the secret is persisted.
func (r *Repository) CreateEnrollment(ctx context.Context, input CreateEnrollmentInput, actor Actor) (Enrollment, string, error) {
	if r.coreKey == nil {
		return Enrollment{}, "", ErrCoreKeyMissing
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if len(input.DisplayName) > 200 {
		return Enrollment{}, "", fmt.Errorf("%w: display name is too long", ErrInvalidInput)
	}
	if input.TTL == 0 {
		input.TTL = DefaultEnrollmentTTL
	}
	if input.TTL < MinEnrollmentTTL || input.TTL > MaxEnrollmentTTL {
		return Enrollment{}, "", fmt.Errorf("%w: expiry must be between 5 minutes and 24 hours", ErrInvalidInput)
	}
	secret, err := agentproto.NewEnrollmentSecret()
	if err != nil {
		return Enrollment{}, "", err
	}
	rawSecret, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return Enrollment{}, "", fmt.Errorf("decode enrollment secret: %w", err)
	}
	digest := sha256.Sum256(rawSecret)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Enrollment{}, "", fmt.Errorf("begin enrollment: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if input.DeviceID != "" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM devices WHERE entity_id = $1::uuid)`, input.DeviceID).Scan(&exists); err != nil {
			return Enrollment{}, "", fmt.Errorf("check enrollment device: %w", err)
		}
		if !exists {
			return Enrollment{}, "", ErrDeviceNotFound
		}
	}
	if input.TargetAgentID != "" {
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM agents WHERE id = $1::uuid`, input.TargetAgentID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return Enrollment{}, "", ErrNotFound
		}
		if err != nil {
			return Enrollment{}, "", fmt.Errorf("check re-enrollment agent: %w", err)
		}
		if status == "removed" {
			return Enrollment{}, "", ErrAgentRemoved
		}
	}

	var enrollmentID string
	err = tx.QueryRow(ctx, `INSERT INTO agent_enrollments (secret_hash, display_name, device_id, target_agent_id, created_by, expires_at)
		VALUES ($1, NULLIF($2, ''), NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5::uuid, now() + $6::interval)
		RETURNING id::text`, digest[:], input.DisplayName, input.DeviceID, input.TargetAgentID, actor.UserID,
		fmt.Sprintf("%d seconds", int64(input.TTL.Seconds()))).Scan(&enrollmentID)
	if err != nil {
		return Enrollment{}, "", fmt.Errorf("create enrollment: %w", err)
	}
	params := map[string]any{"expiresInSeconds": int64(input.TTL.Seconds())}
	if input.DeviceID != "" {
		params["deviceId"] = input.DeviceID
	}
	if input.TargetAgentID != "" {
		params["targetAgentId"] = input.TargetAgentID
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "agent.enrollment.created", TargetType: "agent_enrollment",
		TargetID: enrollmentID, Parameters: params, SourceAddress: actor.SourceAddress}); err != nil {
		return Enrollment{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Enrollment{}, "", fmt.Errorf("commit enrollment: %w", err)
	}
	enrollment, err := r.GetEnrollment(ctx, enrollmentID)
	if err != nil {
		return Enrollment{}, "", err
	}
	return enrollment, agentproto.FormatEnrollmentToken(enrollmentID, secret, r.coreFingerprint()), nil
}

const enrollmentColumns = `e.id::text, COALESCE(e.display_name, ''), COALESCE(e.device_id::text, ''), COALESCE(e.target_agent_id::text, ''),
	e.created_by::text, COALESCE(u.email, ''), e.created_at, e.expires_at, e.used_at, COALESCE(e.enrolled_agent_id::text, ''), e.revoked_at,
	CASE WHEN e.used_at IS NOT NULL THEN 'used' WHEN e.revoked_at IS NOT NULL THEN 'revoked'
	     WHEN e.expires_at <= now() THEN 'expired' ELSE 'pending' END`

func scanEnrollment(row pgx.Row) (Enrollment, error) {
	var e Enrollment
	err := row.Scan(&e.ID, &e.DisplayName, &e.DeviceID, &e.TargetAgentID, &e.CreatedBy, &e.CreatedByEmail,
		&e.CreatedAt, &e.ExpiresAt, &e.UsedAt, &e.EnrolledAgentID, &e.RevokedAt, &e.State)
	return e, err
}

func (r *Repository) GetEnrollment(ctx context.Context, id string) (Enrollment, error) {
	enrollment, err := scanEnrollment(r.db.QueryRow(ctx, `SELECT `+enrollmentColumns+`
		FROM agent_enrollments e LEFT JOIN users u ON u.id = e.created_by WHERE e.id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Enrollment{}, ErrEnrollmentNotFound
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("read enrollment: %w", err)
	}
	return enrollment, nil
}

func (r *Repository) ListEnrollments(ctx context.Context, limit int) ([]Enrollment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.Query(ctx, `SELECT `+enrollmentColumns+`
		FROM agent_enrollments e LEFT JOIN users u ON u.id = e.created_by ORDER BY e.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list enrollments: %w", err)
	}
	defer rows.Close()
	items := make([]Enrollment, 0)
	for rows.Next() {
		enrollment, err := scanEnrollment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan enrollment: %w", err)
		}
		items = append(items, enrollment)
	}
	return items, rows.Err()
}

// RevokeEnrollment invalidates a pending enrollment before it is used.
func (r *Repository) RevokeEnrollment(ctx context.Context, id string, actor Actor) (Enrollment, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Enrollment{}, fmt.Errorf("begin enrollment revocation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `UPDATE agent_enrollments SET revoked_at = now(), revoked_by = $2::uuid
		WHERE id = $1::uuid AND used_at IS NULL AND revoked_at IS NULL AND expires_at > now()`, id, actor.UserID)
	if err != nil {
		return Enrollment{}, fmt.Errorf("revoke enrollment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := r.GetEnrollment(ctx, id); err != nil {
			return Enrollment{}, err
		}
		return Enrollment{}, ErrEnrollmentNotActive
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: "agent.enrollment.revoked", TargetType: "agent_enrollment",
		TargetID: id, SourceAddress: actor.SourceAddress}); err != nil {
		return Enrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Enrollment{}, fmt.Errorf("commit enrollment revocation: %w", err)
	}
	return r.GetEnrollment(ctx, id)
}

// ---------------------------------------------------------------------------
// Enrollment completion

type EnrollResult struct {
	AgentID      string
	CredentialID string
	Fingerprint  string
}

// Enroll consumes an enrollment token exactly once and registers publicKey as
// the agent's only active credential. The caller has already verified that the
// request was signed with publicKey (proof of possession).
func (r *Repository) Enroll(ctx context.Context, token string, publicKey ed25519.PublicKey, facts agentproto.HostFacts, sourceAddress string) (EnrollResult, error) {
	parsed, err := agentproto.ParseEnrollmentToken(token)
	if err != nil {
		return EnrollResult{}, ErrEnrollmentRejected
	}
	enrollmentID, secret := parsed.ID, parsed.Secret
	if err := facts.Validate(); err != nil {
		return EnrollResult{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	normalized := normalizeFacts(facts)
	fingerprint := agentproto.Fingerprint(publicKey)

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return EnrollResult{}, fmt.Errorf("begin agent enrollment: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var (
		storedHash               []byte
		displayName, deviceID    string
		targetAgentID, createdBy string
		used, revoked, expired   bool
	)
	err = tx.QueryRow(ctx, `SELECT secret_hash, COALESCE(display_name, ''), COALESCE(device_id::text, ''),
		COALESCE(target_agent_id::text, ''), created_by::text, used_at IS NOT NULL, revoked_at IS NOT NULL, expires_at <= now()
		FROM agent_enrollments WHERE id = $1::uuid FOR UPDATE`, enrollmentID).Scan(
		&storedHash, &displayName, &deviceID, &targetAgentID, &createdBy, &used, &revoked, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnrollResult{}, ErrEnrollmentRejected
	}
	if err != nil {
		return EnrollResult{}, fmt.Errorf("lock enrollment: %w", err)
	}
	digest := sha256.Sum256(secret)
	reason := ""
	switch {
	case subtle.ConstantTimeCompare(digest[:], storedHash) != 1:
		reason = "invalid_secret"
	case revoked:
		reason = "revoked"
	case used:
		reason = "already_used"
	case expired:
		reason = "expired"
	case parsed.CoreFingerprint != r.coreFingerprint():
		// The token pins a Core key this server no longer uses.
		reason = "core_key_mismatch"
	}
	if reason != "" {
		_ = tx.Rollback(ctx)
		r.auditEnrollmentFailure(ctx, enrollmentID, reason, fingerprint, sourceAddress)
		return EnrollResult{}, ErrEnrollmentRejected
	}

	agentID := targetAgentID
	reenrollment := targetAgentID != ""
	if reenrollment {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM agents WHERE id = $1::uuid FOR UPDATE`, agentID).Scan(&status); err != nil {
			return EnrollResult{}, fmt.Errorf("lock re-enrolled agent: %w", err)
		}
		if status == "removed" {
			_ = tx.Rollback(ctx)
			r.auditEnrollmentFailure(ctx, enrollmentID, "agent_removed", fingerprint, sourceAddress)
			return EnrollResult{}, ErrEnrollmentRejected
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_credentials SET revoked_at = now(), revocation_reason = 'reenrolled'
			WHERE agent_id = $1::uuid AND revoked_at IS NULL`, agentID); err != nil {
			return EnrollResult{}, fmt.Errorf("revoke previous agent credential: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE agents SET status = 'active', revoked_at = NULL, enrolled_at = now(), updated_at = now()
			WHERE id = $1::uuid`, agentID); err != nil {
			return EnrollResult{}, fmt.Errorf("reactivate agent: %w", err)
		}
	} else {
		name := displayName
		if name == "" {
			name = normalized.Hostname
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agents (display_name) VALUES ($1) RETURNING id::text`, truncate(name, 200)).Scan(&agentID); err != nil {
			return EnrollResult{}, fmt.Errorf("create agent: %w", err)
		}
	}
	if err := storeFacts(ctx, tx, agentID, normalized); err != nil {
		return EnrollResult{}, err
	}

	var credentialID string
	err = tx.QueryRow(ctx, `INSERT INTO agent_credentials (agent_id, algorithm, public_key, fingerprint)
		VALUES ($1::uuid, 'ed25519', $2, $3) RETURNING id::text`, agentID, []byte(publicKey), fingerprint).Scan(&credentialID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			_ = tx.Rollback(ctx)
			r.auditEnrollmentFailure(ctx, enrollmentID, "key_reuse", fingerprint, sourceAddress)
			return EnrollResult{}, ErrKeyReuse
		}
		return EnrollResult{}, fmt.Errorf("store agent credential: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_enrollments SET used_at = now(), used_from = NULLIF($2, '')::inet, enrolled_agent_id = $3::uuid
		WHERE id = $1::uuid`, enrollmentID, sourceAddress, agentID); err != nil {
		return EnrollResult{}, fmt.Errorf("consume enrollment: %w", err)
	}

	// An operator who chose a device when creating the enrollment has already
	// confirmed the association. A conflict is kept as visible evidence instead.
	if deviceID != "" {
		if err := r.associateFromEnrollment(ctx, tx, agentID, deviceID, createdBy); err != nil {
			return EnrollResult{}, err
		}
	} else if err := evaluateAssociation(ctx, tx, agentID); err != nil {
		return EnrollResult{}, err
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorAgentID: agentID, Action: "agent.enrollment.completed", TargetType: "agent",
		TargetID: agentID, SourceAddress: sourceAddress, Parameters: map[string]any{
			"enrollmentId": enrollmentID, "enrollmentCreatedBy": createdBy, "credentialId": credentialID,
			"fingerprint": fingerprint, "reenrollment": reenrollment, "hostname": normalized.Hostname,
		}}); err != nil {
		return EnrollResult{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (entity_id, type, severity, category, message, details)
		VALUES ((SELECT device_id FROM agents WHERE id = $1::uuid), 'agent.enrolled', 'info', 'remote-management',
		CASE WHEN $2 THEN 'A management agent was re-enrolled.' ELSE 'A management agent enrolled.' END,
		jsonb_build_object('agentId', $1::text, 'hostname', $3::text))`, agentID, reenrollment, normalized.Hostname); err != nil {
		return EnrollResult{}, fmt.Errorf("record enrollment event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return EnrollResult{}, fmt.Errorf("commit agent enrollment: %w", err)
	}
	return EnrollResult{AgentID: agentID, CredentialID: credentialID, Fingerprint: fingerprint}, nil
}

// auditEnrollmentFailure records rejected attempts against an existing
// enrollment. Attempts with unknown enrollment IDs are not written, so
// unauthenticated callers cannot grow the immutable log arbitrarily.
func (r *Repository) auditEnrollmentFailure(ctx context.Context, enrollmentID, reason, fingerprint, sourceAddress string) {
	_ = audit.Write(ctx, r.db, audit.Entry{Action: "agent.enrollment.rejected", TargetType: "agent_enrollment", TargetID: enrollmentID,
		Result: audit.ResultFailure, ErrorCode: reason, SourceAddress: sourceAddress, Parameters: map[string]any{"fingerprint": fingerprint}})
}

func (r *Repository) associateFromEnrollment(ctx context.Context, tx pgx.Tx, agentID, deviceID, operatorID string) error {
	var deviceName string
	err := tx.QueryRow(ctx, `SELECT e.name FROM devices d JOIN entities e ON e.id = d.entity_id WHERE d.entity_id = $1::uuid FOR UPDATE OF d`, deviceID).Scan(&deviceName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // The device was deleted after the enrollment was created.
	}
	if err != nil {
		return fmt.Errorf("lock enrollment device: %w", err)
	}
	var conflicting string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agents WHERE device_id = $1::uuid AND status <> 'removed' AND id <> $2::uuid`, deviceID, agentID).Scan(&conflicting)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check device agent conflict: %w", err)
	}
	if conflicting != "" {
		evidence := []Evidence{{DeviceID: deviceID, DeviceName: deviceName, Strength: "operator", Signals: []Signal{{Type: "enrollment", Value: "operator-selected"}},
			Note: "The device already has another live agent (" + conflicting + "). Remove or reassign it, then confirm this association."}}
		return setAssociation(ctx, tx, agentID, "", "candidate", evidence, "")
	}
	evidence := []Evidence{{DeviceID: deviceID, DeviceName: deviceName, Strength: "operator", Signals: []Signal{{Type: "enrollment", Value: "operator-selected"}}}}
	return setAssociation(ctx, tx, agentID, deviceID, "confirmed", evidence, operatorID)
}

// setAssociation updates an agent's device link and keeps the 'agent' row in
// device_identities consistent with it, so device identity evidence includes
// the managing agent.
func setAssociation(ctx context.Context, tx pgx.Tx, agentID, deviceID, status string, evidence []Evidence, operatorID string) error {
	if evidence == nil {
		evidence = []Evidence{}
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encode association evidence: %w", err)
	}
	var previousDevice string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(device_id::text, '') FROM agents WHERE id = $1::uuid`, agentID).Scan(&previousDevice); err != nil {
		return fmt.Errorf("read agent association: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM device_identities WHERE identity_type = 'agent' AND value = $1`, agentID); err != nil {
		return fmt.Errorf("clear agent identity: %w", err)
	}
	// An agent that leaves a device takes its reported evidence with it.
	if previousDevice != "" && previousDevice != deviceID {
		if err := devices.ForgetSource(ctx, tx, previousDevice, devices.SourceAgent); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE agents SET device_id = NULLIF($2, '')::uuid, association_status = $3, association_evidence = $4::jsonb,
		associated_at = CASE WHEN NULLIF($2, '') IS NULL THEN NULL ELSE now() END,
		associated_by = NULLIF($5, '')::uuid, updated_at = now() WHERE id = $1::uuid`,
		agentID, deviceID, status, string(encoded), operatorID); err != nil {
		return fmt.Errorf("update agent association: %w", err)
	}
	if deviceID == "" {
		return nil
	}
	confidence := 90
	if status == "confirmed" {
		confidence = 100
	}
	if _, err := tx.Exec(ctx, `INSERT INTO device_identities (device_id, identity_type, value, source, confidence, manually_confirmed_at)
		VALUES ($1::uuid, 'agent', $2, 'nerve-agent', $3, CASE WHEN $4 THEN now() END)`,
		deviceID, agentID, confidence, status == "confirmed"); err != nil {
		return fmt.Errorf("record agent identity: %w", err)
	}
	return syncAgentEvidence(ctx, tx, agentID)
}

// normalizeFacts keeps only syntactically valid, non-loopback addresses and
// produces a deterministic order for digesting.
func normalizeFacts(facts agentproto.HostFacts) agentproto.HostFacts {
	facts.Hostname = strings.TrimSuffix(strings.TrimSpace(facts.Hostname), ".")
	interfaces := make([]agentproto.InterfaceFact, 0, len(facts.Interfaces))
	for _, iface := range facts.Interfaces {
		clean := agentproto.InterfaceFact{Name: strings.TrimSpace(iface.Name), MAC: normalizeMAC(iface.MAC), Addresses: []string{}}
		for _, raw := range iface.Addresses {
			address, err := netip.ParseAddr(strings.TrimSpace(raw))
			if err != nil || address.IsLoopback() || address.IsUnspecified() {
				continue
			}
			clean.Addresses = append(clean.Addresses, address.Unmap().WithZone("").String())
		}
		sort.Strings(clean.Addresses)
		if clean.MAC == "" && len(clean.Addresses) == 0 {
			continue
		}
		interfaces = append(interfaces, clean)
	}
	sort.Slice(interfaces, func(i, j int) bool { return interfaces[i].Name < interfaces[j].Name })
	facts.Interfaces = interfaces
	return facts
}

func factsDigest(facts agentproto.HostFacts) string {
	encoded, _ := json.Marshal(facts)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func storeFacts(ctx context.Context, tx pgx.Tx, agentID string, facts agentproto.HostFacts) error {
	macs := make([]string, 0)
	addresses := make([]string, 0)
	seenMAC := map[string]bool{}
	seenAddress := map[string]bool{}
	for _, iface := range facts.Interfaces {
		if iface.MAC != "" && !seenMAC[iface.MAC] {
			seenMAC[iface.MAC] = true
			macs = append(macs, iface.MAC)
		}
		for _, address := range iface.Addresses {
			if !seenAddress[address] {
				seenAddress[address] = true
				addresses = append(addresses, address)
			}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE agents SET hostname = $2, os = $3, os_version = NULLIF($4, ''), architecture = NULLIF($5, ''),
		agent_version = NULLIF($6, ''), machine_fingerprint = NULLIF($7, ''), reported_mac_addresses = $8::text[]::macaddr[],
		reported_ip_addresses = $9::text[]::inet[], facts_digest = $10, updated_at = now() WHERE id = $1::uuid`,
		agentID, facts.Hostname, facts.OS, facts.OSVersion, facts.Architecture, facts.AgentVersion, facts.MachineFingerprint,
		macs, addresses, factsDigest(facts))
	if err != nil {
		return fmt.Errorf("store agent host facts: %w", err)
	}
	return nil
}

func normalizeMAC(value string) string {
	hardware, err := net.ParseMAC(strings.TrimSpace(value))
	if err != nil || len(hardware) != 6 {
		return ""
	}
	if hardware.String() == "00:00:00:00:00:00" {
		return ""
	}
	return strings.ToLower(hardware.String())
}

// truncate trims and shortens value to at most limit bytes without splitting
// a UTF-8 sequence.
func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// ---------------------------------------------------------------------------
// Request authentication

// Authenticate verifies a signed agent request. Order matters: the signature
// is verified before any write, so unauthenticated callers cannot insert nonces.
func (r *Repository) Authenticate(ctx context.Context, headers agentproto.SignedHeaders, method, route string, body []byte, sourceAddress string) (Principal, error) {
	if err := headers.CheckTimestamp(r.now()); err != nil {
		return Principal{}, err
	}
	if !isUUID(headers.CredentialID) {
		return Principal{}, ErrAuthentication
	}
	var (
		agentID, agentStatus string
		publicKey            []byte
		credentialRevoked    bool
	)
	err := r.db.QueryRow(ctx, `SELECT c.agent_id::text, c.public_key, c.revoked_at IS NOT NULL, a.status
		FROM agent_credentials c JOIN agents a ON a.id = c.agent_id WHERE c.id = $1::uuid`, headers.CredentialID).Scan(
		&agentID, &publicKey, &credentialRevoked, &agentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrAuthentication
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load agent credential: %w", err)
	}
	if err := headers.Verify(ed25519.PublicKey(publicKey), method, route, body); err != nil {
		return Principal{}, ErrAuthentication
	}
	// Only a holder of the private key learns that the credential was revoked.
	if credentialRevoked || agentStatus != "active" {
		return Principal{}, ErrCredentialRevoked
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO agent_request_nonces (credential_id, nonce, expires_at)
		VALUES ($1::uuid, $2, now() + $3::interval) ON CONFLICT DO NOTHING`,
		headers.CredentialID, headers.Nonce, fmt.Sprintf("%d seconds", int64(2*agentproto.MaxClockSkew.Seconds())))
	if err != nil {
		return Principal{}, fmt.Errorf("record agent nonce: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Principal{}, ErrReplay
	}
	if _, err := r.db.Exec(ctx, `UPDATE agents SET last_seen_at = now(), last_ip = NULLIF($2, '')::inet
		WHERE id = $1::uuid AND (last_seen_at IS NULL OR last_seen_at < now() - interval '5 seconds' OR last_ip IS DISTINCT FROM NULLIF($2, '')::inet)`,
		agentID, sourceAddress); err != nil {
		return Principal{}, fmt.Errorf("update agent presence: %w", err)
	}
	return Principal{AgentID: agentID, CredentialID: headers.CredentialID}, nil
}

// PruneNonces deletes replay-protection rows that are past the skew window.
func (r *Repository) PruneNonces(ctx context.Context) error {
	if _, err := r.db.Exec(ctx, `DELETE FROM agent_request_nonces WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("prune agent nonces: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Agent registry

const agentColumns = `a.id::text, a.display_name, a.status,
	COALESCE(a.status = 'active' AND a.last_seen_at > now() - $1::interval
		AND (a.last_shutdown_at IS NULL OR a.last_shutdown_at < a.last_seen_at), false),
	COALESCE(a.hostname, ''), COALESCE(a.os, ''), COALESCE(a.os_version, ''), COALESCE(a.architecture, ''), COALESCE(a.agent_version, ''),
	a.capabilities, a.reported_mac_addresses::text[], ARRAY(SELECT host(x) FROM unnest(a.reported_ip_addresses) x), COALESCE(host(a.last_ip), ''),
	COALESCE(a.last_error, ''), a.first_enrolled_at, a.enrolled_at, a.last_heartbeat_at, a.last_seen_at, a.revoked_at, a.removed_at,
	COALESCE(a.device_id::text, ''), COALESCE(device_entity.name, ''), a.association_status, a.association_evidence,
	a.associated_at, COALESCE(a.associated_by::text, ''),
	COALESCE(credential.id::text, ''), COALESCE(credential.fingerprint, ''), credential.created_at,
	COALESCE(a.run_mode, ''), a.process_started_at, a.host_boot_at, a.last_shutdown_at`

const agentJoins = `FROM agents a
	LEFT JOIN entities device_entity ON device_entity.id = a.device_id
	LEFT JOIN agent_credentials credential ON credential.agent_id = a.id AND credential.revoked_at IS NULL`

func onlineWindow() string { return fmt.Sprintf("%d seconds", int64(OnlineWindow.Seconds())) }

func scanAgent(row pgx.Row) (Agent, error) {
	var (
		agent                     Agent
		deviceID, deviceName      string
		credentialID, fingerprint string
		credentialCreated         *time.Time
		evidence                  []byte
	)
	err := row.Scan(&agent.ID, &agent.DisplayName, &agent.Status, &agent.Online, &agent.Hostname, &agent.OS, &agent.OSVersion,
		&agent.Architecture, &agent.AgentVersion, &agent.Capabilities, &agent.MACAddresses, &agent.IPAddresses, &agent.LastIP,
		&agent.LastError, &agent.FirstEnrolledAt, &agent.EnrolledAt, &agent.LastHeartbeatAt, &agent.LastSeenAt, &agent.RevokedAt,
		&agent.RemovedAt, &deviceID, &deviceName, &agent.Association.Status, &evidence, &agent.Association.AssociatedAt,
		&agent.Association.AssociatedBy, &credentialID, &fingerprint, &credentialCreated, &agent.RunMode,
		&agent.ProcessStartedAt, &agent.HostBootAt, &agent.LastShutdownAt)
	if err != nil {
		return Agent{}, err
	}
	agent.Connection = connectionState(agent)
	if deviceID != "" {
		agent.Device = &DeviceRef{ID: deviceID, Name: deviceName}
	}
	if credentialID != "" && credentialCreated != nil {
		agent.Credential = &Credential{ID: credentialID, Fingerprint: fingerprint, CreatedAt: *credentialCreated}
	}
	agent.Association.Evidence = []Evidence{}
	if len(evidence) > 0 {
		_ = json.Unmarshal(evidence, &agent.Association.Evidence)
	}
	if agent.Capabilities == nil {
		agent.Capabilities = []string{}
	}
	if agent.MACAddresses == nil {
		agent.MACAddresses = []string{}
	}
	if agent.IPAddresses == nil {
		agent.IPAddresses = []string{}
	}
	return agent, nil
}

type ListFilter struct {
	DeviceID       string
	IncludeRemoved bool
}

func (r *Repository) List(ctx context.Context, filter ListFilter) ([]Agent, error) {
	rows, err := r.db.Query(ctx, `SELECT `+agentColumns+` `+agentJoins+`
		WHERE (NULLIF($2, '') IS NULL OR a.device_id = NULLIF($2, '')::uuid)
		  AND ($3 OR a.status <> 'removed')
		ORDER BY a.status = 'removed', a.display_name, a.id LIMIT 500`, onlineWindow(), filter.DeviceID, filter.IncludeRemoved)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	items := make([]Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		items = append(items, agent)
	}
	return items, rows.Err()
}

func (r *Repository) Get(ctx context.Context, id string) (Agent, error) {
	agent, err := scanAgent(r.db.QueryRow(ctx, `SELECT `+agentColumns+` `+agentJoins+` WHERE a.id = $2::uuid`, onlineWindow(), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("read agent: %w", err)
	}
	return agent, nil
}

// LifecycleHook lets other packages react inside the same transaction when an
// agent loses its credential (for example to cancel its pending commands).
type LifecycleHook func(ctx context.Context, tx pgx.Tx, agentID string, actor Actor, reason string) error

// Revoke disables the agent's active credential. The agent keeps its identity
// and history and can be re-enrolled with a new enrollment token.
func (r *Repository) Revoke(ctx context.Context, agentID string, actor Actor, hooks ...LifecycleHook) (Agent, error) {
	return r.endCredential(ctx, agentID, actor, "revoked", hooks)
}

// Remove permanently retires an agent: credentials are revoked, the device
// association is cleared and the agent can never authenticate again. The row
// is kept because audit records and command history reference it.
func (r *Repository) Remove(ctx context.Context, agentID string, actor Actor, hooks ...LifecycleHook) (Agent, error) {
	return r.endCredential(ctx, agentID, actor, "removed", hooks)
}

func (r *Repository) endCredential(ctx context.Context, agentID string, actor Actor, mode string, hooks []LifecycleHook) (Agent, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return Agent{}, fmt.Errorf("begin agent %s: %w", mode, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM agents WHERE id = $1::uuid FOR UPDATE`, agentID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, fmt.Errorf("lock agent: %w", err)
	}
	if status == "removed" {
		return Agent{}, ErrAgentRemoved
	}
	var revokedCredentials []string
	rows, err := tx.Query(ctx, `UPDATE agent_credentials SET revoked_at = now(), revoked_by = $2::uuid, revocation_reason = $3
		WHERE agent_id = $1::uuid AND revoked_at IS NULL RETURNING id::text`, agentID, actor.UserID, mode)
	if err != nil {
		return Agent{}, fmt.Errorf("revoke agent credentials: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return Agent{}, err
		}
		revokedCredentials = append(revokedCredentials, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Agent{}, err
	}
	if mode == "removed" {
		if err := setAssociation(ctx, tx, agentID, "", "unassociated", nil, ""); err != nil {
			return Agent{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE agents SET status = 'removed', removed_at = now(), revoked_at = COALESCE(revoked_at, now()), updated_at = now() WHERE id = $1::uuid`, agentID); err != nil {
			return Agent{}, fmt.Errorf("remove agent: %w", err)
		}
	} else {
		if status == "revoked" && len(revokedCredentials) == 0 {
			return Agent{}, ErrCredentialRevoked
		}
		if _, err := tx.Exec(ctx, `UPDATE agents SET status = 'revoked', revoked_at = now(), updated_at = now() WHERE id = $1::uuid`, agentID); err != nil {
			return Agent{}, fmt.Errorf("revoke agent: %w", err)
		}
	}
	for _, hook := range hooks {
		if err := hook(ctx, tx, agentID, actor, mode); err != nil {
			return Agent{}, err
		}
	}
	action := "agent.credential.revoked"
	if mode == "removed" {
		action = "agent.removed"
	}
	if err := audit.Write(ctx, tx, audit.Entry{ActorUserID: actor.UserID, Action: action, TargetType: "agent", TargetID: agentID,
		SourceAddress: actor.SourceAddress, Parameters: map[string]any{"revokedCredentialIds": revokedCredentials}}); err != nil {
		return Agent{}, err
	}
	message := "A management agent credential was revoked."
	if mode == "removed" {
		message = "A management agent was removed."
	}
	if _, err := tx.Exec(ctx, `INSERT INTO events (type, severity, category, message, details)
		VALUES ($1, 'warning', 'remote-management', $2, jsonb_build_object('agentId', $3::text))`, "agent."+mode, message, agentID); err != nil {
		return Agent{}, fmt.Errorf("record agent lifecycle event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Agent{}, fmt.Errorf("commit agent %s: %w", mode, err)
	}
	return r.Get(ctx, agentID)
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdef", char) {
				return false
			}
		}
	}
	return true
}

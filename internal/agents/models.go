// Package agents owns management-agent identity: enrollment tokens, per-agent
// Ed25519 credentials, signed-request authentication, lifecycle (revoke,
// re-enroll, remove), heartbeats and device association.
package agents

import (
	"errors"
	"time"
)

const (
	// OnlineWindow is how recently an agent must have made an authenticated
	// request to count as online. Agents heartbeat every 30 seconds.
	OnlineWindow = 90 * time.Second
	// HeartbeatInterval is advertised to agents at enrollment.
	HeartbeatInterval = 30 * time.Second

	DefaultEnrollmentTTL = time.Hour
	MaxEnrollmentTTL     = 24 * time.Hour
	MinEnrollmentTTL     = 5 * time.Minute

	DefaultDeploymentKeyTTL = 7 * 24 * time.Hour
	MaxDeploymentKeyTTL     = 90 * 24 * time.Hour
	MinDeploymentKeyTTL     = time.Hour
	MaxDeploymentKeyUses    = 10000

	ApprovalManual    = "manual"
	ApprovalAutomatic = "automatic"
)

var (
	ErrNotFound            = errors.New("agent not found")
	ErrEnrollmentNotFound  = errors.New("enrollment not found")
	ErrEnrollmentRejected  = errors.New("enrollment token is invalid, expired, revoked or already used")
	ErrEnrollmentNotActive = errors.New("enrollment is no longer pending")
	ErrDeploymentKeyNotFound  = errors.New("deployment key not found")
	ErrDeploymentKeyNotActive = errors.New("deployment key is already revoked or expired")
	ErrNotPendingApproval     = errors.New("agent is not waiting for approval")
	ErrAwaitingApproval       = errors.New("agent is waiting for approval")
	ErrKeyReuse            = errors.New("this public key is already registered")
	ErrInvalidInput        = errors.New("invalid input")
	ErrAgentRemoved        = errors.New("agent was removed")
	ErrDeviceNotFound      = errors.New("device not found")
	ErrDeviceHasAgent      = errors.New("device is already associated with another live agent")
	ErrCoreKeyMissing      = errors.New("NERVE_CORE_SIGNING_KEY is not configured; run setup.bat or set it in .env")

	ErrAuthentication    = errors.New("agent authentication failed")
	ErrCredentialRevoked = errors.New("agent credential was revoked")
	ErrReplay            = errors.New("agent request was replayed")
)

// Principal is an authenticated agent request.
type Principal struct {
	AgentID      string
	CredentialID string
}

type DeviceRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Credential struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Evidence struct {
	DeviceID   string   `json:"deviceId"`
	DeviceName string   `json:"deviceName,omitempty"`
	Signals    []Signal `json:"signals"`
	Strength   string   `json:"strength"`
	Note       string   `json:"note,omitempty"`
}

type Signal struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Association struct {
	Status       string     `json:"status"`
	Evidence     []Evidence `json:"evidence"`
	AssociatedAt *time.Time `json:"associatedAt,omitempty"`
	AssociatedBy string     `json:"associatedBy,omitempty"`
}

type Agent struct {
	ID               string      `json:"id"`
	DisplayName      string      `json:"displayName"`
	Status           string      `json:"status"`
	Online           bool        `json:"online"`
	Connection       string      `json:"connection"`
	RunMode          string      `json:"runMode,omitempty"`
	ProcessStartedAt *time.Time  `json:"processStartedAt,omitempty"`
	HostBootAt       *time.Time  `json:"hostBootAt,omitempty"`
	LastShutdownAt   *time.Time  `json:"lastShutdownAt,omitempty"`
	Hostname         string      `json:"hostname,omitempty"`
	OS               string      `json:"os,omitempty"`
	OSVersion        string      `json:"osVersion,omitempty"`
	Architecture     string      `json:"architecture,omitempty"`
	AgentVersion     string      `json:"agentVersion,omitempty"`
	Capabilities     []string    `json:"capabilities"`
	MACAddresses     []string    `json:"macAddresses"`
	IPAddresses      []string    `json:"ipAddresses"`
	LastIP           string      `json:"lastIp,omitempty"`
	LastError        string      `json:"lastError,omitempty"`
	FirstEnrolledAt  time.Time   `json:"firstEnrolledAt"`
	EnrolledAt       time.Time   `json:"enrolledAt"`
	LastHeartbeatAt  *time.Time  `json:"lastHeartbeatAt,omitempty"`
	LastSeenAt       *time.Time  `json:"lastSeenAt,omitempty"`
	RevokedAt        *time.Time  `json:"revokedAt,omitempty"`
	RemovedAt        *time.Time  `json:"removedAt,omitempty"`
	Device           *DeviceRef  `json:"device,omitempty"`
	Association      Association `json:"association"`
	Credential       *Credential `json:"credential,omitempty"`
	DeploymentKeyID  string      `json:"deploymentKeyId,omitempty"`
}

type Enrollment struct {
	ID              string     `json:"id"`
	DisplayName     string     `json:"displayName,omitempty"`
	DeviceID        string     `json:"deviceId,omitempty"`
	TargetAgentID   string     `json:"targetAgentId,omitempty"`
	CreatedBy       string     `json:"createdBy"`
	CreatedByEmail  string     `json:"createdByEmail,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	ExpiresAt       time.Time  `json:"expiresAt"`
	State           string     `json:"state"`
	UsedAt          *time.Time `json:"usedAt,omitempty"`
	EnrolledAgentID string     `json:"enrolledAgentId,omitempty"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
}

// DeploymentKey is a reusable, limited enrollment secret. The secret itself is
// shown once at creation; only its digest is stored.
type DeploymentKey struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Approval       string     `json:"approval"`
	MaxUses        int        `json:"maxUses"`
	UsedCount      int        `json:"usedCount"`
	CreatedBy      string     `json:"createdBy"`
	CreatedByEmail string     `json:"createdByEmail,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	LastUsedAt     *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	State          string     `json:"state"`
}

// CreateDeploymentKeyInput is validated by CreateDeploymentKey.
type CreateDeploymentKeyInput struct {
	Name     string
	Approval string
	MaxUses  int
	TTL      time.Duration
}

// CreateEnrollmentInput is validated by CreateEnrollment.
type CreateEnrollmentInput struct {
	DisplayName   string
	DeviceID      string
	TargetAgentID string
	TTL           time.Duration
}

// Actor identifies the operator performing an administrative action.
type Actor struct {
	UserID        string
	SourceAddress string
}

// Connection states derived from presence and the agent's own shutdown report.
const (
	ConnectionOnline    = "online"
	ConnectionOffline   = "offline"
	ConnectionStopped   = "stopped"
	ConnectionNeverSeen = "never_seen"
	ConnectionInactive  = "inactive"
)

// connectionState explains why an agent is or is not online: "stopped" means
// the agent reported a graceful shutdown; "offline" means it went silent.
func connectionState(agent Agent) string {
	switch {
	case agent.Status != "active" && agent.Status != "pending":
		return ConnectionInactive
	case agent.Online:
		return ConnectionOnline
	case agent.LastSeenAt == nil:
		return ConnectionNeverSeen
	case agent.LastShutdownAt != nil && !agent.LastShutdownAt.Before(*agent.LastSeenAt):
		return ConnectionStopped
	default:
		return ConnectionOffline
	}
}

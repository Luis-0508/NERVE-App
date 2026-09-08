package discovery

import "time"

type Target struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type AgentStatus struct {
	ID              string    `json:"id"`
	Hostname        string    `json:"hostname"`
	Platform        string    `json:"platform"`
	Version         string    `json:"version"`
	Capabilities    []string  `json:"capabilities"`
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
	LastError       string    `json:"lastError,omitempty"`
	Online          bool      `json:"online"`
}

type Scan struct {
	ID                 string      `json:"id"`
	TargetID           string      `json:"targetId"`
	TargetCIDR         string      `json:"targetCidr"`
	AgentID            string      `json:"agentId,omitempty"`
	Status             string      `json:"status"`
	RequestedAt        time.Time   `json:"requestedAt"`
	StartedAt          *time.Time  `json:"startedAt,omitempty"`
	CompletedAt        *time.Time  `json:"completedAt,omitempty"`
	AddressesTotal     int         `json:"addressesTotal"`
	AddressesReachable int         `json:"addressesReachable"`
	ErrorCount         int         `json:"errorCount"`
	ErrorMessage       string      `json:"errorMessage,omitempty"`
	Errors             []ScanError `json:"errors"`
}

type ScanError struct {
	Address    string    `json:"address,omitempty"`
	Stage      string    `json:"stage"`
	Message    string    `json:"message"`
	OccurredAt time.Time `json:"occurredAt"`
}

type State struct {
	Target *Target      `json:"target,omitempty"`
	Agent  *AgentStatus `json:"agent,omitempty"`
	Scan   *Scan        `json:"scan,omitempty"`
}

type Job struct {
	ID   string `json:"id"`
	CIDR string `json:"cidr"`
}

type AgentHeartbeat struct {
	ID           string   `json:"id"`
	Hostname     string   `json:"hostname"`
	Platform     string   `json:"platform"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	LastError    string   `json:"lastError,omitempty"`
}

type Observation struct {
	Address   string `json:"address"`
	Reachable bool   `json:"reachable"`
	Hostname  string `json:"hostname,omitempty"`
	MAC       string `json:"mac,omitempty"`
	Method    string `json:"method"`
}

type Report struct {
	Observations []Observation `json:"observations"`
	Errors       []ScanError   `json:"errors"`
}

type Completion struct {
	AddressesTotal int    `json:"addressesTotal"`
	Status         string `json:"status"`
	ErrorMessage   string `json:"errorMessage,omitempty"`
}

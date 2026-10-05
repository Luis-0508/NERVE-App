package inventory

import (
	"time"

	"github.com/nerve-labs/nerve/internal/devices"
)

type DeviceListParams struct {
	Search         string
	Status         string
	Classification string
	Trust          string
	Tag            string
	Sort           string
	Direction      string
	Limit          int
	Offset         int
}

type DeviceList struct {
	Items  []DeviceSummary `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

type DeviceFacets struct {
	Statuses        []string `json:"statuses"`
	Classifications []string `json:"classifications"`
	TrustStates     []string `json:"trustStates"`
	Tags            []string `json:"tags"`
}

// DeviceSummary is the effective device shown to users. Name and Classification
// are resolved by Core: an operator override wins over discovered values.
type DeviceSummary struct {
	ID                       string    `json:"id"`
	Name                     string    `json:"name"`
	DisplayName              string    `json:"displayName,omitempty"`
	DiscoveredName           string    `json:"discoveredName,omitempty"`
	Status                   string    `json:"status"`
	Classification           string    `json:"classification"`
	DiscoveredClassification string    `json:"discoveredClassification"`
	ClassificationOverridden bool      `json:"classificationOverridden"`
	TrustState               string    `json:"trustState"`
	Tags                     []string  `json:"tags"`
	Hostname                 string    `json:"hostname,omitempty"`
	PrimaryIPAddress         string    `json:"primaryIpAddress,omitempty"`
	PrimaryMAC               string    `json:"primaryMac,omitempty"`
	Vendor                   string    `json:"vendor,omitempty"`
	OperatingSystem          string    `json:"operatingSystem,omitempty"`
	FirstSeenAt              time.Time `json:"firstSeenAt"`
	LastSeenAt               time.Time `json:"lastSeenAt"`
}

type Device struct {
	DeviceSummary
	Model        string     `json:"model,omitempty"`
	SerialNumber string     `json:"serialNumber,omitempty"`
	Owner        string     `json:"owner,omitempty"`
	Location     string     `json:"location,omitempty"`
	Notes        string     `json:"notes,omitempty"`
	ApprovedAt   *time.Time `json:"approvedAt,omitempty"`

	MetadataUpdatedAt *time.Time `json:"metadataUpdatedAt,omitempty"`
	MetadataUpdatedBy string     `json:"metadataUpdatedBy,omitempty"`
	TrustChangedAt    *time.Time `json:"trustChangedAt,omitempty"`
	TrustChangedBy    string     `json:"trustChangedBy,omitempty"`
	MergedInto        string     `json:"mergedInto,omitempty"`
}

type Interface struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	MACAddress string      `json:"macAddress,omitempty"`
	MediaType  string      `json:"mediaType,omitempty"`
	MTU        *int32      `json:"mtu,omitempty"`
	SpeedBPS   *int64      `json:"speedBps,omitempty"`
	IsUp       *bool       `json:"isUp,omitempty"`
	Addresses  []IPAddress `json:"addresses"`
}

type IPAddress struct {
	ID            string     `json:"id"`
	Address       string     `json:"address"`
	Version       int16      `json:"version"`
	IsPrimary     bool       `json:"isPrimary"`
	InterfaceID   string     `json:"interfaceId"`
	InterfaceName string     `json:"interfaceName"`
	ValidFrom     time.Time  `json:"validFrom"`
	ValidUntil    *time.Time `json:"validUntil,omitempty"`
}

type ServiceEndpoint struct {
	ID          string    `json:"id"`
	ServiceID   string    `json:"serviceId,omitempty"`
	Name        string    `json:"name"`
	ServiceType string    `json:"serviceType,omitempty"`
	Protocol    string    `json:"protocol"`
	Port        int32     `json:"port"`
	Exposure    string    `json:"exposure"`
	State       string    `json:"state"`
	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

type RelatedEntity struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type Relationship struct {
	ID                string        `json:"id"`
	Type              string        `json:"type"`
	Direction         string        `json:"direction"`
	RelatedEntity     RelatedEntity `json:"relatedEntity"`
	Source            string        `json:"source"`
	Confidence        int16         `json:"confidence"`
	FirstSeenAt       time.Time     `json:"firstSeenAt"`
	LastSeenAt        time.Time     `json:"lastSeenAt"`
	ManuallyConfirmed bool          `json:"manuallyConfirmed"`
}

type DeviceDetail struct {
	Device        Device            `json:"device"`
	Interfaces    []Interface       `json:"interfaces"`
	IPAddresses   []IPAddress       `json:"ipAddresses"`
	Services      []ServiceEndpoint `json:"services"`
	Relationships []Relationship    `json:"relationships"`
	// Facts lists every sourced claim (provenance) behind the discovered values.
	Facts []devices.Attribute `json:"facts"`
}

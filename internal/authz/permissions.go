// Package authz names the server-side permissions. The role-to-permission
// mapping lives in PostgreSQL (role_permissions), so adding a role or narrowing
// one is a data change rather than a code change.
package authz

type Permission string

const (
	DiscoveryManage         Permission = "discovery.manage"
	AgentsView              Permission = "agents.view"
	AgentsManage            Permission = "agents.manage"
	RemoteView              Permission = "remote.view"
	RemoteExecuteReadOnly   Permission = "remote.execute.read_only"
	RemoteExecutePrivileged Permission = "remote.execute.privileged"
	AuditView               Permission = "audit.view"
)

// Set is an immutable permission set loaded for one authenticated user.
type Set map[Permission]struct{}

func NewSet(keys ...string) Set {
	set := make(Set, len(keys))
	for _, key := range keys {
		set[Permission(key)] = struct{}{}
	}
	return set
}

func (s Set) Has(permission Permission) bool {
	_, ok := s[permission]
	return ok
}

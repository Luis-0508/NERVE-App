// Package audit writes and reads the immutable audit_log. Callers pass only
// safe parameters: identifiers, types, sizes and outcomes. Secrets, credentials
// and command output never belong in an audit record; command results live in
// their own table and are referenced by command ID.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const (
	ResultSuccess = "success"
	ResultFailure = "failure"
	ResultDenied  = "denied"
)

// Entry is one audit record. Exactly one of ActorUserID or ActorAgentID is
// normally set; both empty means a system action (for example an expiry sweep).
type Entry struct {
	ActorUserID   string
	ActorAgentID  string
	Action        string
	TargetType    string
	TargetID      string
	Parameters    map[string]any
	Result        string
	ErrorCode     string
	SourceAddress string
}

func Write(ctx context.Context, db Execer, entry Entry) error {
	parameters := entry.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return fmt.Errorf("encode audit parameters: %w", err)
	}
	if entry.Result == "" {
		entry.Result = ResultSuccess
	}
	_, err = db.Exec(ctx, `INSERT INTO audit_log
		(actor_id, actor_agent_id, action, target_type, target_id, safe_parameters, result, error_code, source_address)
		VALUES (NULLIF($1, '')::uuid, NULLIF($2, '')::uuid, $3, $4, NULLIF($5, ''), $6::jsonb, $7, NULLIF($8, ''), NULLIF($9, '')::inet)`,
		entry.ActorUserID, entry.ActorAgentID, entry.Action, entry.TargetType, entry.TargetID, string(encoded), entry.Result, entry.ErrorCode, entry.SourceAddress)
	if err != nil {
		return fmt.Errorf("write audit record %s: %w", entry.Action, err)
	}
	return nil
}

type Record struct {
	ID            int64          `json:"id"`
	ActorUserID   string         `json:"actorUserId,omitempty"`
	ActorEmail    string         `json:"actorEmail,omitempty"`
	ActorAgentID  string         `json:"actorAgentId,omitempty"`
	Action        string         `json:"action"`
	TargetType    string         `json:"targetType"`
	TargetID      string         `json:"targetId,omitempty"`
	Parameters    map[string]any `json:"parameters"`
	Result        string         `json:"result"`
	ErrorCode     string         `json:"errorCode,omitempty"`
	SourceAddress string         `json:"sourceAddress,omitempty"`
	OccurredAt    time.Time      `json:"occurredAt"`
}

type Filter struct {
	TargetType   string
	TargetID     string
	ActionPrefix string
	Limit        int
}

func List(ctx context.Context, db Querier, filter Filter) ([]Record, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	rows, err := db.Query(ctx, `SELECT log.id, COALESCE(log.actor_id::text, ''), COALESCE(actor.email, ''),
		COALESCE(log.actor_agent_id::text, ''), log.action, log.target_type, COALESCE(log.target_id, ''),
		log.safe_parameters, log.result, COALESCE(log.error_code, ''), COALESCE(host(log.source_address), ''), log.occurred_at
		FROM audit_log log LEFT JOIN users actor ON actor.id = log.actor_id
		WHERE (NULLIF($1, '') IS NULL OR log.target_type = $1)
		  AND (NULLIF($2, '') IS NULL OR log.target_id = $2)
		  AND (NULLIF($3, '') IS NULL OR log.action LIKE $3 || '%')
		ORDER BY log.occurred_at DESC, log.id DESC LIMIT $4`, filter.TargetType, filter.TargetID, filter.ActionPrefix, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("query audit log: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0)
	for rows.Next() {
		var record Record
		if err := rows.Scan(&record.ID, &record.ActorUserID, &record.ActorEmail, &record.ActorAgentID, &record.Action,
			&record.TargetType, &record.TargetID, &record.Parameters, &record.Result, &record.ErrorCode,
			&record.SourceAddress, &record.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan audit record: %w", err)
		}
		if record.Parameters == nil {
			record.Parameters = map[string]any{}
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

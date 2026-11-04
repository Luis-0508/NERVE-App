package remote

import (
	"context"
	"fmt"
	"time"

	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/redact"
)

// SnapshotTypes are the read-only inventory commands the RMM device page (#93)
// shows. Each section displays the newest retained result of its type.
var SnapshotTypes = []string{
	agentproto.CapabilitySystemInfo,
	agentproto.CapabilitySystemMetrics,
	agentproto.CapabilityProcessList,
	agentproto.CapabilityServiceList,
	agentproto.CapabilitySoftwareList,
}

// Snapshots returns, per snapshot type, the newest succeeded read-only command
// for a device (including devices merged into it) whose result is still
// retained. Types without such a result are absent. The data is exactly what
// the agent reported, with the requesting operator and time as provenance.
func (s *Service) Snapshots(ctx context.Context, deviceID string) ([]Command, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (c.command_type) `+commandColumns+`, r.output, r.output_truncated,
		r.output_bytes, r.result_digest, r.submitted_at, r.redactions
		FROM remote_commands c JOIN agents a ON a.id = c.agent_id LEFT JOIN users u ON u.id = c.requested_by
		JOIN remote_command_results r ON r.command_id = c.id
		WHERE c.status = 'succeeded' AND c.risk = $2 AND c.command_type = ANY($3)
		  AND (c.device_id = $1::uuid OR c.device_id IN (SELECT entity_id FROM devices WHERE merged_into = $1::uuid))
		ORDER BY c.command_type, r.submitted_at DESC, c.id`, deviceID, string(agentproto.RiskReadOnly), SnapshotTypes)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer rows.Close()
	items := make([]Command, 0, len(SnapshotTypes))
	for rows.Next() {
		var (
			output      []byte
			truncated   bool
			outputBytes int
			digest      string
			submittedAt time.Time
			redactions  redact.Counts
		)
		command, err := scanCommand(rows, &output, &truncated, &outputBytes, &digest, &submittedAt, &redactions)
		if err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		command.Result = &Result{Output: output, OutputTruncated: truncated, OutputBytes: outputBytes, Digest: digest,
			SubmittedAt: submittedAt, Redactions: redactions}
		items = append(items, command)
	}
	return items, rows.Err()
}

package standalone

import (
	"context"
	"time"
)

type AuditEvent struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Actor   string    `json:"actor"`
	Action  string    `json:"action"`
	Target  string    `json:"target"`
	Outcome string    `json:"outcome"`
	Detail  string    `json:"detail,omitempty"`
}

func (m *Manager) Audit(ctx context.Context, actor, action, target, outcome, detail string) {
	_, _ = m.store.DB.ExecContext(ctx, `INSERT INTO audit_log(at,actor,action,target,outcome,detail)
		VALUES(?,?,?,?,?,?)`, time.Now().UTC().Format(time.RFC3339Nano), actor, action, target, outcome, detail)
}

func (m *Manager) AuditEvents(ctx context.Context, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := m.store.DB.QueryContext(ctx, `SELECT id,at,actor,action,target,outcome,detail
		FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var at string
		if err := rows.Scan(&event.ID, &at, &event.Actor, &event.Action, &event.Target, &event.Outcome, &event.Detail); err != nil {
			return nil, err
		}
		if parsed, err := time.Parse(time.RFC3339Nano, at); err == nil {
			event.At = parsed
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

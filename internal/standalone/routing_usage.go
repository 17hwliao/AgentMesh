package standalone

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"agentmesh/internal/catalog"
)

type RoutingMemberInput struct {
	ModelID  string `json:"model_id"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type RoutingGroupInput struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Enabled     *bool                `json:"enabled,omitempty"`
	Members     []RoutingMemberInput `json:"members"`
}

var groupNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

func (m *Manager) UpsertRoutingGroup(ctx context.Context, input RoutingGroupInput) (catalog.RoutingGroup, error) {
	name := strings.TrimSpace(input.Name)
	if !groupNamePattern.MatchString(name) {
		return catalog.RoutingGroup{}, errors.New("routing_group_name_invalid")
	}
	data, err := m.loadData(ctx)
	if err != nil {
		return catalog.RoutingGroup{}, err
	}
	knownModels := map[string]bool{}
	for _, model := range data.Models {
		knownModels[model.ID] = true
		if model.PublicID == name {
			return catalog.RoutingGroup{}, errors.New("routing_group_name_conflicts_with_model")
		}
	}
	if len(input.Members) == 0 {
		return catalog.RoutingGroup{}, errors.New("routing_group_members_required")
	}
	seen := map[string]bool{}
	members := make([]catalog.RoutingMember, 0, len(input.Members))
	for _, member := range input.Members {
		if !knownModels[member.ModelID] || seen[member.ModelID] {
			return catalog.RoutingGroup{}, errors.New("routing_group_member_invalid")
		}
		seen[member.ModelID] = true
		enabled := true
		if member.Enabled != nil {
			enabled = *member.Enabled
		}
		weight := member.Weight
		if weight <= 0 {
			weight = 1
		}
		members = append(members, catalog.RoutingMember{
			Group: name, ModelID: member.ModelID, Priority: member.Priority, Weight: weight, Enabled: enabled,
		})
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	err = m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO routing_groups(name,description,enabled)
			VALUES(?,?,?) ON CONFLICT(name) DO UPDATE SET description=excluded.description,enabled=excluded.enabled`,
			name, input.Description, boolIntForSQL(enabled)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM routing_members WHERE group_name=?`, name); err != nil {
			return err
		}
		for _, member := range members {
			if _, err := tx.ExecContext(ctx, `INSERT INTO routing_members
				(group_name,model_id,priority,weight,enabled) VALUES(?,?,?,?,?)`,
				member.Group, member.ModelID, member.Priority, member.Weight, boolIntForSQL(member.Enabled)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return catalog.RoutingGroup{}, err
	}
	return catalog.RoutingGroup{Name: name, Description: input.Description, Enabled: enabled, Members: members}, nil
}

func (m *Manager) DeleteRoutingGroup(ctx context.Context, name string) error {
	return m.mutate(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM routing_members WHERE group_name=?`, name); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM routing_groups WHERE name=?`, name)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count == 0 {
			return catalog.ErrNotFound
		}
		return nil
	})
}

type UsageSummary struct {
	Rows      []UsageRow         `json:"rows"`
	Cooldowns []CooldownRow      `json:"cooldowns"`
	Window    UsageWindowSummary `json:"window"`
}

type UsageRow struct {
	KeyID        string `json:"key_id"`
	ProviderID   string `json:"provider_id"`
	Model        string `json:"model"`
	Requests     int    `json:"requests"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

type CooldownRow struct {
	ProviderID string    `json:"provider_id"`
	Model      string    `json:"model"`
	Until      time.Time `json:"until"`
	Reason     string    `json:"reason"`
}

type UsageWindowSummary struct {
	MinuteRequests int `json:"minute_requests"`
	DayRequests    int `json:"day_requests"`
	DayTokens      int `json:"day_tokens"`
}

func (m *Manager) UsageSummary(ctx context.Context) (UsageSummary, error) {
	day := time.Now().UTC().Truncate(24 * time.Hour)
	rows, err := m.store.DB.QueryContext(ctx, `SELECT key_id,provider_id,model,COUNT(*),
		COALESCE(SUM(input_tokens),0),COALESCE(SUM(output_tokens),0)
		FROM usage_events WHERE created_at>=? GROUP BY key_id,provider_id,model
		ORDER BY COUNT(*) DESC,model LIMIT 100`, day.Format(time.RFC3339Nano))
	if err != nil {
		return UsageSummary{}, err
	}
	defer rows.Close()
	out := UsageSummary{}
	for rows.Next() {
		var row UsageRow
		if err := rows.Scan(&row.KeyID, &row.ProviderID, &row.Model, &row.Requests, &row.InputTokens, &row.OutputTokens); err != nil {
			return UsageSummary{}, err
		}
		out.Rows = append(out.Rows, row)
	}
	m.usageMu.Lock()
	for _, window := range m.usage {
		out.Window.MinuteRequests += window.minuteReq
		out.Window.DayRequests += window.dayReq
		out.Window.DayTokens += window.dayTokens
	}
	m.usageMu.Unlock()
	coolRows, err := m.store.DB.QueryContext(ctx, `SELECT provider_id,model,until,reason
		FROM cooldowns WHERE until>? ORDER BY until`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return UsageSummary{}, err
	}
	defer coolRows.Close()
	for coolRows.Next() {
		var row CooldownRow
		var until string
		if err := coolRows.Scan(&row.ProviderID, &row.Model, &until, &row.Reason); err != nil {
			return UsageSummary{}, err
		}
		row.Until, _ = time.Parse(time.RFC3339Nano, until)
		out.Cooldowns = append(out.Cooldowns, row)
	}
	return out, nil
}

func boolIntForSQL(value bool) int {
	if value {
		return 1
	}
	return 0
}

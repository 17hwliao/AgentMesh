package standalone

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"agentmesh/internal/provider"
)

type keyUsageWindow struct {
	minuteStart time.Time
	minuteReq   int
	dayStart    time.Time
	dayReq      int
	dayTokens   int
}

func (m *Manager) Allow(_ context.Context, keyID, model string) bool {
	snapshot := m.Snapshot()
	if snapshot == nil {
		return false
	}
	key, ok := snapshot.KeyByID[keyID]
	if !ok || !key.Enabled || !modelAllowed(key, model) {
		return false
	}
	now := time.Now().UTC()
	m.usageMu.Lock()
	defer m.usageMu.Unlock()
	window := m.usageWindow(keyID, now)
	if key.RPM > 0 && window.minuteReq >= key.RPM {
		return false
	}
	if key.DailyRequests > 0 && window.dayReq >= key.DailyRequests {
		return false
	}
	if key.DailyTokens > 0 && window.dayTokens >= key.DailyTokens {
		return false
	}
	if m.dailyTokenLimit > 0 {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		total := 0
		for _, item := range m.usage {
			if item.dayStart.Equal(day) {
				total += item.dayTokens
			}
		}
		if total >= m.dailyTokenLimit {
			return false
		}
	}
	return true
}

func (m *Manager) RecordUsage(ctx context.Context, keyID, providerID, model string, inputTokens, outputTokens int, result string) {
	now := time.Now().UTC()
	m.usageMu.Lock()
	window := m.usageWindow(keyID, now)
	window.minuteReq++
	window.dayReq++
	window.dayTokens += inputTokens + outputTokens
	m.usageMu.Unlock()
	if _, err := m.store.DB.ExecContext(ctx, `INSERT INTO usage_events
		(key_id,provider_id,model,input_tokens,output_tokens,result,created_at)
		VALUES(?,?,?,?,?,?,?)`, keyID, providerID, model, inputTokens, outputTokens, result, now.Format(time.RFC3339Nano)); err != nil {
		// Runtime counters remain useful for local admission even if a local
		// disk write fails; the error is deliberately not exposed to the client.
		return
	}
}

func (m *Manager) usageWindow(keyID string, now time.Time) *keyUsageWindow {
	window := m.usage[keyID]
	if window == nil {
		window = &keyUsageWindow{}
		m.usage[keyID] = window
	}
	minute := now.Truncate(time.Minute)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if !window.minuteStart.Equal(minute) {
		window.minuteStart, window.minuteReq = minute, 0
	}
	if !window.dayStart.Equal(day) {
		window.dayStart, window.dayReq, window.dayTokens = day, 0, 0
	}
	return window
}

func (m *Manager) loadUsageCounters(ctx context.Context) {
	now := time.Now().UTC()
	minute := now.Truncate(time.Minute)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	rows, err := m.store.DB.QueryContext(ctx, `SELECT key_id,
		SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),
		SUM(CASE WHEN created_at>=? THEN 1 ELSE 0 END),
		SUM(CASE WHEN created_at>=? THEN input_tokens+output_tokens ELSE 0 END)
		FROM usage_events GROUP BY key_id`, minute.Format(time.RFC3339Nano), day.Format(time.RFC3339Nano), day.Format(time.RFC3339Nano))
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var keyID string
		var minuteReq, dayReq, dayTokens sql.NullInt64
		if rows.Scan(&keyID, &minuteReq, &dayReq, &dayTokens) != nil {
			return
		}
		m.usage[keyID] = &keyUsageWindow{
			minuteStart: minute, minuteReq: int(minuteReq.Int64),
			dayStart: day, dayReq: int(dayReq.Int64), dayTokens: int(dayTokens.Int64),
		}
	}
}

func (m *Manager) recordCooldown(providerID, model string, err error) {
	status, hasStatus := provider.UpstreamStatus(err)
	var until time.Time
	reason := "upstream_error"
	switch {
	case status == 401 || status == 403:
		until = time.Now().UTC().Add(10 * time.Minute)
		reason = "credential_rejected"
	case status == 429:
		until = time.Now().UTC().Add(time.Minute)
		reason = "rate_limited"
	case hasStatus && status >= 500:
		until = time.Now().UTC().Add(30 * time.Second)
		reason = "upstream_unavailable"
	case errors.Is(err, provider.ErrProtocol):
		until = time.Now().UTC().Add(15 * time.Second)
		reason = "protocol_error"
	default:
		return
	}
	key := cooldownKey(providerID, model)
	m.cooldownMu.Lock()
	m.cooldowns[key] = until
	m.cooldownMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = m.store.DB.ExecContext(ctx, `INSERT INTO cooldowns(provider_id,model,until,reason)
		VALUES(?,?,?,?) ON CONFLICT(provider_id,model) DO UPDATE SET until=excluded.until,reason=excluded.reason`,
		providerID, model, until.Format(time.RFC3339Nano), reason)
}

func (m *Manager) clearCooldown(providerID, model string) {
	key := cooldownKey(providerID, model)
	m.cooldownMu.Lock()
	delete(m.cooldowns, key)
	m.cooldownMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = m.store.DB.ExecContext(ctx, `DELETE FROM cooldowns WHERE provider_id=? AND model=?`, providerID, model)
}

func (m *Manager) cooldownActive(providerID, model string) bool {
	key := cooldownKey(providerID, model)
	m.cooldownMu.RLock()
	until, ok := m.cooldowns[key]
	m.cooldownMu.RUnlock()
	if !ok {
		return false
	}
	if time.Now().UTC().After(until) {
		m.cooldownMu.Lock()
		delete(m.cooldowns, key)
		m.cooldownMu.Unlock()
		return false
	}
	return true
}

func (m *Manager) loadCooldowns(ctx context.Context) {
	rows, err := m.store.DB.QueryContext(ctx, `SELECT provider_id,model,until FROM cooldowns`)
	if err != nil {
		return
	}
	defer rows.Close()
	now := time.Now().UTC()
	for rows.Next() {
		var providerID, model, raw string
		if rows.Scan(&providerID, &model, &raw) != nil {
			return
		}
		until, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil && until.After(now) {
			m.cooldowns[cooldownKey(providerID, model)] = until
		}
	}
}

func cooldownKey(providerID, model string) string {
	return providerID + "\x00" + model
}

func (m *Manager) Cooldowns() map[string]time.Time {
	now := time.Now().UTC()
	m.cooldownMu.RLock()
	defer m.cooldownMu.RUnlock()
	out := make(map[string]time.Time)
	for key, until := range m.cooldowns {
		if until.After(now) {
			parts := strings.SplitN(key, "\x00", 2)
			out[key] = until
			_ = parts
		}
	}
	return out
}

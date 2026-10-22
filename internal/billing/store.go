package billing

import (
	"agentmesh/internal/provider"
	"agentmesh/internal/reservation"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type Store struct {
	DB      *sql.DB
	Catalog Catalog
	Version string
}

func (s *Store) RecordCacheHit(ctx context.Context, tenant, model string) error {
	if e := s.Health(ctx); e != nil {
		return e
	}
	priced := false
	for _, p := range s.Catalog.Prices {
		if p.Model == model {
			priced = true
			break
		}
	}
	if !priced {
		return ErrInvalid
	}
	if !identifier.MatchString(tenant) || !identifier.MatchString(model) {
		return ErrInvalid
	}
	idBytes := make([]byte, 16)
	if _, e := rand.Read(idBytes); e != nil {
		return e
	}
	id := hex.EncodeToString(idBytes)
	_, e := s.DB.ExecContext(ctx, "INSERT INTO billing_requests(request_id,tenant_id,currency,model,price_version,state,reserved_nano) VALUES (?,?,?,?,?,'cache_hit',0)", id, tenant, s.Catalog.Currency, model, s.Version)
	return e
}

func New(ctx context.Context, db *sql.DB, raw []byte) (*Store, error) {
	c, v, e := Decode(raw)
	if e != nil {
		return nil, e
	}
	data, _ := json.Marshal(c)
	if _, e = db.ExecContext(ctx, "INSERT IGNORE INTO billing_catalogs(version,catalog) VALUES (?,?)", v, data); e != nil {
		return nil, e
	}
	return &Store{DB: db, Catalog: c, Version: v}, nil
}
func (s *Store) Health(ctx context.Context) error {
	if !s.Catalog.ExpiresAt.After(time.Now()) {
		return ErrInvalid
	}
	return s.DB.PingContext(ctx)
}
func (s *Store) Provision(ctx context.Context, tenant string) error {
	_, e := s.DB.ExecContext(ctx, "INSERT IGNORE INTO billing_wallets(tenant_id,currency) SELECT tenant_id,? FROM tenants WHERE tenant_id=? AND enabled=1", s.Catalog.Currency, tenant)
	return e
}
func (s *Store) Credit(ctx context.Context, tenant, id, proof string, amount uint64) error {
	if !identifier.MatchString(tenant) || !identifier.MatchString(id) || len(proof) < 8 || len(proof) > 256 || amount == 0 || amount > MaxMoney {
		return ErrInvalid
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var enabled bool
	if e = tx.QueryRowContext(ctx, "SELECT enabled FROM tenants WHERE tenant_id=? FOR UPDATE", tenant).Scan(&enabled); e != nil || !enabled {
		return ErrInvalid
	}
	var priorTenant, priorCurrency, priorProof string
	var prior uint64
	e = tx.QueryRowContext(ctx, "SELECT tenant_id,currency,amount_nano,proof FROM billing_credits WHERE credit_id=?", id).Scan(&priorTenant, &priorCurrency, &prior, &priorProof)
	if e == nil {
		if priorTenant != tenant || priorCurrency != s.Catalog.Currency || prior != amount || priorProof != proof {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT IGNORE INTO billing_wallets(tenant_id,currency) VALUES (?,?)", tenant, s.Catalog.Currency); e != nil {
		return e
	}
	var balance uint64
	if e = tx.QueryRowContext(ctx, "SELECT balance_nano FROM billing_wallets WHERE tenant_id=? AND currency=? FOR UPDATE", tenant, s.Catalog.Currency).Scan(&balance); e != nil {
		return e
	}
	if balance > MaxMoney || amount > MaxMoney-balance {
		return ErrInvalid
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO billing_credits(credit_id,tenant_id,currency,amount_nano,proof) VALUES (?,?,?,?,?)", id, tenant, s.Catalog.Currency, amount, proof); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE billing_wallets SET balance_nano=balance_nano+? WHERE tenant_id=? AND currency=?", amount, tenant, s.Catalog.Currency); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO billing_audit(request_id,action,proof) VALUES (?,'credit',?)", id, proof); e != nil {
		return e
	}
	return tx.Commit()
}

func (s *Store) Begin(ctx context.Context, tenant, model string, messages []provider.Message) (reservation.StreamSession, error) {
	if !identifier.MatchString(tenant) || !identifier.MatchString(model) || len(messages) == 0 || len(messages) > 129 || s.Health(ctx) != nil {
		return nil, ErrInvalid
	}
	// No guessed tokenizer is used for the fee. This byte bound is admission
	// only; reservation covers the configured maximum token count per attempt.
	size := provider.InputBound(messages)
	if size > 64<<10 {
		return nil, ErrInvalid
	}
	var maximum uint64
	found := false
	for _, p := range s.Catalog.Prices {
		if p.Model != model {
			continue
		}
		if size > p.MaxInput {
			return nil, ErrInvalid
		}
		cost, e := p.Cost(provider.Usage{InputTokens: p.MaxInput, OutputTokens: MaxOutputTokens})
		if e != nil {
			return nil, e
		}
		if cost > maximum {
			maximum = cost
		}
		found = true
	}
	if !found || maximum > MaxMoney/3 {
		return nil, ErrInvalid
	}
	reserve := maximum * 3
	idBytes := make([]byte, 16)
	if _, e := rand.Read(idBytes); e != nil {
		return nil, e
	}
	id := hex.EncodeToString(idBytes)
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var balance, held uint64
	if e = tx.QueryRowContext(ctx, "SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id=? AND currency=? FOR UPDATE", tenant, s.Catalog.Currency).Scan(&balance, &held); e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			return nil, ErrInsufficient
		}
		return nil, e
	}
	if balance < held || reserve > balance-held {
		return nil, ErrInsufficient
	}
	if _, e = tx.ExecContext(ctx, "UPDATE billing_wallets SET held_nano=held_nano+? WHERE tenant_id=? AND currency=?", reserve, tenant, s.Catalog.Currency); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO billing_requests(request_id,tenant_id,currency,model,price_version,state,reserved_nano) VALUES (?,?,?,?,?,'held',?)", id, tenant, s.Catalog.Currency, model, s.Version, reserve); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return &session{store: s, id: id, model: model, tenant: tenant}, nil
}

type session struct {
	store                       *Store
	id, model, tenant, provider string
	ordinal                     int
	done                        bool
}

func (s *session) OutputLimit() int  { return MaxOutputTokens }
func (s *session) BillingID() string { return s.id }
func (s *session) BeforeAttempt(ctx context.Context, name string, r provider.ChatRequest) error {
	if s.done || r.Model != s.model || s.ordinal >= 3 {
		return ErrInvalid
	}
	if _, ok := s.store.Catalog.Price(name, s.model); !ok {
		return ErrInvalid
	}
	tx, e := s.store.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var state string
	if e = tx.QueryRowContext(ctx, "SELECT state FROM billing_requests WHERE request_id=? FOR UPDATE", s.id).Scan(&state); e != nil || state != "held" {
		return ErrConflict
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO billing_attempts(request_id,ordinal,provider) VALUES (?,?,?)", s.id, s.ordinal+1, name); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE billing_requests SET updated_at=UTC_TIMESTAMP(6) WHERE request_id=?", s.id); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.ordinal++
	s.provider = name
	return nil
}
func (s *session) BeforeEmit(ctx context.Context, name string, c provider.Chunk) error {
	if s.done || s.ordinal == 0 || name != s.provider {
		return ErrInvalid
	}
	if c.Usage == nil {
		return nil
	}
	price, ok := s.store.Catalog.Price(name, s.model)
	if !ok {
		return ErrInvalid
	}
	if _, e := price.Cost(*c.Usage); e != nil {
		return e
	}
	raw, e := json.Marshal(c.Usage)
	if e != nil {
		return e
	}
	// The usage is stored before forwarding; duplicate reports must be equal.
	tx, e := s.store.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var prior []byte
	if e = tx.QueryRowContext(ctx, "SELECT usage_json FROM billing_attempts WHERE request_id=? AND ordinal=? FOR UPDATE", s.id, s.ordinal).Scan(&prior); e != nil {
		return e
	}
	if len(prior) > 0 {
		var u provider.Usage
		if json.Unmarshal(prior, &u) != nil || u != *c.Usage {
			return ErrConflict
		}
		return tx.Commit()
	}
	if _, e = tx.ExecContext(ctx, "UPDATE billing_attempts SET usage_json=? WHERE request_id=? AND ordinal=?", raw, s.id, s.ordinal); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *session) AfterEmit(context.Context, string, provider.Chunk) error { return nil }
func (s *session) FinishAttempt(ctx context.Context, name, outcome string) error {
	if s.done || s.ordinal == 0 || name != s.provider || len(outcome) > 64 {
		return ErrInvalid
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, e := s.store.DB.ExecContext(cleanup, "UPDATE billing_attempts SET outcome=? WHERE request_id=? AND ordinal=? AND outcome IS NULL", outcome, s.id, s.ordinal)
	return e
}
func (s *session) Complete(ctx context.Context, _ string, _ bool) error {
	if s.done {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	e := s.store.finalize(cleanup, s.id, nil, "")
	if e == nil {
		s.done = true
	}
	return e
}

type Resolution struct {
	Ordinal int            `json:"ordinal"`
	Usage   provider.Usage `json:"usage"`
}

func (s *Store) Resolve(ctx context.Context, id, proof string, items []Resolution) error {
	if !identifier.MatchString(id) || len(id) != 32 || len(proof) < 8 || len(proof) > 256 || len(items) == 0 || len(items) > 3 {
		return ErrInvalid
	}
	return s.finalize(ctx, id, items, proof)
}
func (s *Store) finalize(ctx context.Context, id string, resolve []Resolution, proof string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var tenant, currency, model, version, state string
	var reserve uint64
	if e = tx.QueryRowContext(ctx, "SELECT tenant_id,currency,model,price_version,state,reserved_nano FROM billing_requests WHERE request_id=? FOR UPDATE", id).Scan(&tenant, &currency, &model, &version, &state, &reserve); e != nil {
		return e
	}
	if state == "settled" || state == "cancelled" {
		if resolve != nil {
			return ErrConflict
		}
		return tx.Commit()
	}
	if resolve != nil && state != "pending_reconciliation" {
		return ErrConflict
	}
	var rawCatalog []byte
	if e = tx.QueryRowContext(ctx, "SELECT catalog FROM billing_catalogs WHERE version=?", version).Scan(&rawCatalog); e != nil {
		return e
	}
	// Expired historical versions remain valid for already-opened requests.
	var catalog Catalog
	if json.Unmarshal(rawCatalog, &catalog) != nil {
		return ErrInvalid
	}
	rows, e := tx.QueryContext(ctx, "SELECT ordinal,provider,usage_json,outcome FROM billing_attempts WHERE request_id=? ORDER BY ordinal FOR UPDATE", id)
	if e != nil {
		return e
	}
	type attempt struct {
		ordinal int
		name    string
		raw     []byte
		outcome sql.NullString
	}
	var attempts []attempt
	for rows.Next() {
		var a attempt
		if e = rows.Scan(&a.ordinal, &a.name, &a.raw, &a.outcome); e != nil {
			rows.Close()
			return e
		}
		attempts = append(attempts, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	overrides := map[int]provider.Usage{}
	for _, v := range resolve {
		if v.Ordinal < 1 || v.Ordinal > len(attempts) {
			return ErrInvalid
		}
		if _, ok := overrides[v.Ordinal]; ok {
			return ErrInvalid
		}
		overrides[v.Ordinal] = v.Usage
	}
	var charge, input, output uint64
	known := true
	for _, a := range attempts {
		var u provider.Usage
		if v, ok := overrides[a.ordinal]; ok {
			if len(a.raw) > 0 {
				var prior provider.Usage
				if json.Unmarshal(a.raw, &prior) != nil || prior != v {
					return ErrConflict
				}
			}
			u = v
		} else if len(a.raw) == 0 || json.Unmarshal(a.raw, &u) != nil || !a.outcome.Valid {
			known = false
			continue
		}
		p, ok := catalog.Price(a.name, model)
		if !ok {
			return ErrInvalid
		}
		cost, err := p.Cost(u)
		if err != nil {
			known = false
			continue
		}
		if charge > reserve || cost > reserve-charge {
			return ErrInvalid
		}
		charge += cost
		input += uint64(u.InputTokens)
		output += uint64(u.OutputTokens)
	}
	if !known {
		if resolve != nil {
			return ErrInvalid
		}
		_, e = tx.ExecContext(ctx, "UPDATE billing_requests SET state='pending_reconciliation',updated_at=UTC_TIMESTAMP(6) WHERE request_id=?", id)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	var balance, held uint64
	if e = tx.QueryRowContext(ctx, "SELECT balance_nano,held_nano FROM billing_wallets WHERE tenant_id=? AND currency=? FOR UPDATE", tenant, currency).Scan(&balance, &held); e != nil {
		return e
	}
	if held < reserve || balance < charge {
		return ErrInvalid
	}
	if _, e = tx.ExecContext(ctx, "UPDATE billing_wallets SET balance_nano=balance_nano-?,held_nano=held_nano-? WHERE tenant_id=? AND currency=?", charge, reserve, tenant, currency); e != nil {
		return e
	}
	final := "settled"
	if len(attempts) == 0 {
		final = "cancelled"
	}
	if _, e = tx.ExecContext(ctx, "UPDATE billing_requests SET state=?,charged_nano=?,input_tokens=?,output_tokens=?,updated_at=UTC_TIMESTAMP(6) WHERE request_id=?", final, charge, input, output, id); e != nil {
		return e
	}
	for ordinal, u := range overrides {
		raw, _ := json.Marshal(u)
		if _, e = tx.ExecContext(ctx, "UPDATE billing_attempts SET usage_json=?,outcome=COALESCE(outcome,'verified') WHERE request_id=? AND ordinal=?", raw, id, ordinal); e != nil {
			return e
		}
	}
	if proof == "" {
		proof = "provider_observed"
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO billing_audit(request_id,action,proof) VALUES (?,?,?)", id, final, proof); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Recover(ctx context.Context) error {
	rows, e := s.DB.QueryContext(ctx, "SELECT request_id FROM billing_requests WHERE state='held' AND updated_at<UTC_TIMESTAMP(6)-INTERVAL 5 MINUTE LIMIT 100")
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = s.finalize(ctx, id, nil, ""); e != nil {
			return e
		}
	}
	return nil
}

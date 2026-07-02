package governor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type UsageEvent struct {
	RequestID     string        `json:"request_id"`
	KeyID         string        `json:"key_id"`
	KeyPreview    string        `json:"key_preview"`
	Model         string        `json:"model"`
	Endpoint      string        `json:"endpoint,omitempty"`
	RequestedAt   time.Time     `json:"requested_at"`
	LatencyMS     int64         `json:"latency_ms"`
	Failed        bool          `json:"failed"`
	Failure       string        `json:"failure,omitempty"`
	Usage         TokenUsage    `json:"usage"`
	Cost          float64       `json:"cost"`
	CostBreakdown CostBreakdown `json:"cost_breakdown"`
}

func OpenStore(path string) (*Store, error) {
	if path == "" {
		path = "cpa-governor.sqlite"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		`pragma journal_mode=wal`,
		`create table if not exists keys (
			id text primary key,
			name text not null,
			key_hash text not null unique,
			enabled integer not null,
			preview text,
			rpm integer,
			concurrency integer,
			models_json text,
			prices_json text,
			five_hour_limit_usd real,
			daily_limit_usd real,
			weekly_limit_usd real,
			monthly_limit_usd real,
			updated_at integer not null
		)`,
		`create table if not exists reset_watermarks (
			key_id text not null,
			window text not null,
			reset_at integer not null,
			primary key(key_id, window)
		)`,
		`create table if not exists usage_events (
			id integer primary key autoincrement,
			request_id text,
			key_id text,
			key_preview text,
			model text,
			endpoint text,
			requested_at integer not null,
			latency_ms integer,
			failed integer not null,
			failure text,
			input_tokens integer,
			output_tokens integer,
			cached_tokens integer,
			cache_read_tokens integer,
			cache_creation_tokens integer,
			reasoning_tokens integer,
			total_tokens integer,
			cost real,
			cost_breakdown_json text
		)`,
		`create table if not exists codexcont_summaries (
			request_id text primary key,
			key_id text,
			model text,
			protection text,
			summary_json text,
			updated_at integer not null
		)`,
		`create table if not exists audit_log (
			id integer primary key autoincrement,
			timestamp integer not null,
			actor text,
			action text not null,
			target text,
			detail_json text
		)`,
		`create table if not exists settings (
			key text primary key,
			value text not null,
			updated_at integer not null
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertKey(ctx context.Context, key KeyRecord) error {
	models, _ := json.Marshal(key.Models)
	prices, _ := json.Marshal(key.Prices)
	_, err := s.db.ExecContext(
		ctx,
		`insert into keys(
			id, name, key_hash, enabled, preview, rpm, concurrency, models_json, prices_json,
			five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, updated_at
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			name=excluded.name,
			key_hash=excluded.key_hash,
			enabled=excluded.enabled,
			preview=excluded.preview,
			rpm=excluded.rpm,
			concurrency=excluded.concurrency,
			models_json=excluded.models_json,
			prices_json=excluded.prices_json,
			five_hour_limit_usd=coalesce(keys.five_hour_limit_usd, excluded.five_hour_limit_usd),
			daily_limit_usd=excluded.daily_limit_usd,
			weekly_limit_usd=excluded.weekly_limit_usd,
			monthly_limit_usd=coalesce(keys.monthly_limit_usd, excluded.monthly_limit_usd),
			updated_at=excluded.updated_at`,
		key.ID,
		key.Name,
		key.KeyHash,
		boolInt(key.Enabled),
		key.Preview,
		key.RPM,
		key.Concurrency,
		string(models),
		string(prices),
		key.FiveHourUSD,
		key.DailyLimitUSD,
		key.WeeklyLimitUSD,
		key.MonthlyLimitUSD,
		time.Now().Unix(),
	)
	return err
}

func (s *Store) ImportKeys(ctx context.Context, state KeyPolicyState) error {
	seen := make(map[string]bool, len(state.Keys))
	for _, key := range state.Keys {
		seen[key.ID] = true
		if err := s.UpsertKey(ctx, key); err != nil {
			return err
		}
	}
	if err := s.syncDeletedKeys(ctx, seen); err != nil {
		return err
	}
	return nil
}

func (s *Store) syncDeletedKeys(ctx context.Context, seen map[string]bool) error {
	rows, err := s.db.QueryContext(ctx, `select id from keys`)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := s.db.ExecContext(ctx, `delete from keys where id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListKeys(ctx context.Context) ([]KeyRecord, error) {
	rows, err := s.db.QueryContext(ctx, `select id, name, key_hash, enabled, preview, rpm, concurrency, models_json, prices_json,
		five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd from keys order by name collate nocase`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyRecord
	for rows.Next() {
		var key KeyRecord
		var enabled int
		var modelsJSON, pricesJSON string
		var fiveHour, daily, weekly, monthly sql.NullFloat64
		if err := rows.Scan(
			&key.ID, &key.Name, &key.KeyHash, &enabled, &key.Preview, &key.RPM, &key.Concurrency,
			&modelsJSON, &pricesJSON, &fiveHour, &daily, &weekly, &monthly,
		); err != nil {
			return nil, err
		}
		key.Enabled = enabled != 0
		key.FiveHourUSD = nullFloatPtr(fiveHour)
		key.DailyLimitUSD = nullFloatPtr(daily)
		key.WeeklyLimitUSD = nullFloatPtr(weekly)
		key.MonthlyLimitUSD = nullFloatPtr(monthly)
		_ = json.Unmarshal([]byte(modelsJSON), &key.Models)
		_ = json.Unmarshal([]byte(pricesJSON), &key.Prices)
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *Store) FindKeyByHash(ctx context.Context, hash string) (KeyRecord, bool, error) {
	keys, err := s.ListKeys(ctx)
	if err != nil {
		return KeyRecord{}, false, err
	}
	normalized, err := NormalizeHash(hash)
	if err != nil {
		return KeyRecord{}, false, nil
	}
	for _, key := range keys {
		keyHash, err := NormalizeHash(key.KeyHash)
		if err == nil && keyHash == normalized {
			return key, true, nil
		}
	}
	return KeyRecord{}, false, nil
}

func (s *Store) SetLimits(ctx context.Context, id string, fiveHour, monthly *float64) error {
	res, err := s.db.ExecContext(ctx, `update keys set five_hour_limit_usd=?, monthly_limit_usd=?, updated_at=? where id=?`, fiveHour, monthly, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", id)
	}
	return s.Audit(ctx, "admin", "set_limits", id, map[string]any{"five_hour_usd": fiveHour, "monthly_usd": monthly})
}

func (s *Store) Reset(ctx context.Context, id, window string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into reset_watermarks(key_id, window, reset_at) values(?, ?, ?)
		on conflict(key_id, window) do update set reset_at=excluded.reset_at`, id, window, at.Unix())
	if err != nil {
		return err
	}
	return s.Audit(ctx, "admin", "reset_usage", id, map[string]any{"window": window, "reset_at": at.Unix()})
}

func (s *Store) InsertUsage(ctx context.Context, event UsageEvent) error {
	if event.RequestedAt.IsZero() {
		event.RequestedAt = time.Now()
	}
	breakdown, _ := json.Marshal(event.CostBreakdown)
	_, err := s.db.ExecContext(
		ctx,
		`insert into usage_events(
			request_id, key_id, key_preview, model, endpoint, requested_at, latency_ms, failed, failure,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, cost, cost_breakdown_json
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID, event.KeyID, event.KeyPreview, event.Model, event.Endpoint, event.RequestedAt.Unix(),
		event.LatencyMS, boolInt(event.Failed), event.Failure,
		event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CachedTokens, event.Usage.CacheReadTokens,
		event.Usage.CacheCreationTokens, event.Usage.ReasoningTokens, event.Usage.TotalTokens,
		event.Cost, string(breakdown),
	)
	return err
}

func (s *Store) UsageSum(ctx context.Context, keyID string, window Window) (float64, error) {
	var total sql.NullFloat64
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	args := []any{from, window.To.Unix()}
	query := `select coalesce(sum(cost), 0) from usage_events where requested_at >= ? and requested_at <= ?`
	if keyID != "" && keyID != "all" {
		query += ` and key_id = ?`
		args = append(args, keyID)
	}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, err
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

func (s *Store) ResetAt(ctx context.Context, keyID, window string) (int64, bool) {
	if s == nil || s.db == nil || keyID == "" || window == "" || keyID == "all" {
		return 0, false
	}
	var resetAt sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `select reset_at from reset_watermarks where key_id=? and window=?`, keyID, window).Scan(&resetAt); err != nil {
		return 0, false
	}
	return resetAt.Int64, resetAt.Valid
}

func (s *Store) RecentEvents(ctx context.Context, keyID string, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select request_id, key_id, key_preview, model, endpoint, requested_at, latency_ms, failed, failure,
		input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost, cost_breakdown_json
		from usage_events`
	args := []any{}
	if keyID != "" && keyID != "all" {
		query += ` where key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by requested_at desc, id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageEvent
	for rows.Next() {
		var event UsageEvent
		var ts int64
		var failed int
		var breakdown string
		if err := rows.Scan(
			&event.RequestID, &event.KeyID, &event.KeyPreview, &event.Model, &event.Endpoint, &ts, &event.LatencyMS, &failed, &event.Failure,
			&event.Usage.InputTokens, &event.Usage.OutputTokens, &event.Usage.CachedTokens, &event.Usage.CacheReadTokens,
			&event.Usage.CacheCreationTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens, &event.Cost, &breakdown,
		); err != nil {
			return nil, err
		}
		event.RequestedAt = time.Unix(ts, 0)
		event.Failed = failed != 0
		_ = json.Unmarshal([]byte(breakdown), &event.CostBreakdown)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) RecentEventsWindow(ctx context.Context, keyID string, window Window, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	query := `select request_id, key_id, key_preview, model, endpoint, requested_at, latency_ms, failed, failure,
		input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost, cost_breakdown_json
		from usage_events where requested_at >= ? and requested_at <= ?`
	args := []any{from, window.To.Unix()}
	if keyID != "" && keyID != "all" {
		query += ` and key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by requested_at desc, id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageEvent
	for rows.Next() {
		var event UsageEvent
		var ts int64
		var failed int
		var breakdown string
		if err := rows.Scan(
			&event.RequestID, &event.KeyID, &event.KeyPreview, &event.Model, &event.Endpoint, &ts, &event.LatencyMS, &failed, &event.Failure,
			&event.Usage.InputTokens, &event.Usage.OutputTokens, &event.Usage.CachedTokens, &event.Usage.CacheReadTokens,
			&event.Usage.CacheCreationTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens, &event.Cost, &breakdown,
		); err != nil {
			return nil, err
		}
		event.RequestedAt = time.Unix(ts, 0)
		event.Failed = failed != 0
		_ = json.Unmarshal([]byte(breakdown), &event.CostBreakdown)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) SaveCodexSummary(ctx context.Context, requestID, keyID, model, protection string, summary any) error {
	raw, _ := json.Marshal(summary)
	_, err := s.db.ExecContext(ctx, `insert into codexcont_summaries(request_id, key_id, model, protection, summary_json, updated_at)
		values(?, ?, ?, ?, ?, ?)
		on conflict(request_id) do update set key_id=excluded.key_id, model=excluded.model,
		protection=excluded.protection, summary_json=excluded.summary_json, updated_at=excluded.updated_at`,
		requestID, keyID, model, protection, string(raw), time.Now().Unix())
	return err
}

func (s *Store) Audit(ctx context.Context, actor, action, target string, detail any) error {
	raw, _ := json.Marshal(detail)
	_, err := s.db.ExecContext(ctx, `insert into audit_log(timestamp, actor, action, target, detail_json) values(?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, target, string(raw))
	return err
}

func (s *Store) LoadSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `select key, value from settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (s *Store) SaveSettings(ctx context.Context, values map[string]string) error {
	for key, value := range values {
		if _, err := s.db.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?, ?, ?)
			on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`,
			key, value, time.Now().Unix()); err != nil {
			return err
		}
	}
	return s.Audit(ctx, "admin", "set_settings", "governor", values)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullFloatPtr(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	v := value.Float64
	return &v
}

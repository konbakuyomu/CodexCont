package policyplus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type UsageEvent struct {
	ID              int64         `json:"-"`
	RequestID       string        `json:"request_id"`
	KeyID           string        `json:"key_id"`
	KeyPreview      string        `json:"key_preview"`
	Model           string        `json:"model"`
	RequestedModel  string        `json:"requested_model,omitempty"`
	ActualModel     string        `json:"actual_model,omitempty"`
	Provider        string        `json:"provider,omitempty"`
	ExecutorType    string        `json:"executor_type,omitempty"`
	Endpoint        string        `json:"endpoint,omitempty"`
	RequestedAt     time.Time     `json:"requested_at"`
	LatencyMS       int64         `json:"latency_ms"`
	TTFTMS          int64         `json:"ttft_ms,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	ServiceTier     string        `json:"service_tier,omitempty"`
	StatusCode      int           `json:"status_code,omitempty"`
	Failed          bool          `json:"failed"`
	Failure         string        `json:"failure,omitempty"`
	Usage           TokenUsage    `json:"usage"`
	Cost            float64       `json:"cost"`
	CostBreakdown   CostBreakdown `json:"cost_breakdown"`
	BillingPending  bool          `json:"billing_pending,omitempty"`
	BillingReason   string        `json:"billing_pending_reason,omitempty"`
	BillingModel    string        `json:"billing_price_model,omitempty"`
	// AuthIndex is the upstream credential that served the request, used to
	// attribute carpool (pool) consumption to one subscription account.
	AuthIndex string `json:"auth_index,omitempty"`
	AuthID    string `json:"-"`
	// ConsumedAtMS is when the upstream finished the request (requested_at +
	// latency, millisecond precision); pool attribution orders costs by it.
	ConsumedAtMS int64 `json:"-"`
}

type UsageSummary struct {
	Calls          int64      `json:"calls"`
	Failed         int64      `json:"failed"`
	BillingPending int64      `json:"billing_pending"`
	TotalCost      float64    `json:"total_cost"`
	Usage          TokenUsage `json:"usage"`
}

type CodexSummary struct {
	RequestID  string         `json:"request_id"`
	KeyID      string         `json:"key_id,omitempty"`
	Model      string         `json:"model,omitempty"`
	Protection string         `json:"protection,omitempty"`
	Summary    map[string]any `json:"summary"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type LegacyImportResult struct {
	Limits int
	Resets int
}

const sqliteBusyTimeoutMS = 5000

func OpenStore(path string) (*Store, error) {
	if path == "" {
		path = "cpa-policyplus.sqlite"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return nil, err
	}
	db, err := openSQLite(path, false)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.EnsureSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	dsn := sqliteDSN(path, readOnly)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if readOnly {
		db.SetMaxOpenConns(2)
		db.SetMaxIdleConns(1)
	}
	return db, nil
}

func sqliteDSN(path string, readOnly bool) string {
	path = strings.TrimSpace(path)
	query := url.Values{}
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	if readOnly {
		query.Set("mode", "ro")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
	}
	if strings.HasPrefix(path, "file:") {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		return path + sep + query.Encode()
	}
	if readOnly {
		return "file:" + filepath.ToSlash(path) + "?" + query.Encode()
	}
	return path + "?" + query.Encode()
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
			max_active_sessions integer,
			models_json text,
			prices_json text,
			five_hour_limit_usd real,
			daily_limit_usd real,
			weekly_limit_usd real,
			monthly_limit_usd real,
			weekly_only integer not null default 0,
			quota_unlimited integer not null default 0,
			archived integer not null default 0,
			archived_at integer not null default 0,
			source text default '',
			source_present integer not null default 1,
			alias text default '',
			inherited_from text default '',
			inherit_conflict integer not null default 0,
			hidden integer not null default 0,
			last_enabled integer not null default 0,
			billing_hold_reason text not null default '',
			billing_hold_model text not null default '',
			billing_hold_service_tier text not null default '',
			billing_hold_at integer not null default 0,
			updated_at integer not null
		)`,
		`create table if not exists reset_watermarks (
			key_id text not null,
			window text not null,
			reset_at integer not null,
			primary key(key_id, window)
		)`,
		`create table if not exists active_sessions (
			key_id text not null,
			session_id text not null,
			source text,
			first_seen integer not null,
			last_seen integer not null,
			primary key(key_id, session_id)
		)`,
		`create table if not exists usage_events (
			id integer primary key autoincrement,
			request_id text,
			key_id text,
			key_preview text,
			model text,
			requested_model text,
			actual_model text,
			provider text,
			executor_type text,
			endpoint text,
			requested_at integer not null,
			latency_ms integer,
			ttft_ms integer,
			reasoning_effort text,
			service_tier text,
			status_code integer,
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
			cost_breakdown_json text,
			billing_pending integer not null default 0,
			billing_pending_reason text not null default '',
			billing_price_model text not null default ''
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
		`create table if not exists funding_misses (
			key_id text not null,
			model text not null,
			provider text not null default '',
			reason text not null default '',
			count integer not null default 0,
			first_at integer not null,
			last_at integer not null,
			primary key(key_id, model)
		)`,
		`create table if not exists quota_accounts (
			auth_index text primary key,
			auth_id text not null default '',
			provider text not null default '',
			plan_type text not null default '',
			weekly_minutes integer not null default 0,
			weekly_used real,
			weekly_reset_at integer not null default 0,
			short_minutes integer not null default 0,
			short_used real,
			short_reset_at integer not null default 0,
			limit_reached integer not null default 0,
			observed_at_ms integer not null default 0,
			updated_at integer not null default 0
		)`,
		`create table if not exists quota_observations (
			auth_index text not null,
			cycle_reset_at integer not null,
			used_percent real not null,
			observed_at_ms integer not null,
			window_minutes integer not null default 0,
			primary key(auth_index, cycle_reset_at, used_percent)
		)`,
		`create index if not exists idx_quota_observations_time on quota_observations(auth_index, observed_at_ms)`,
		`create table if not exists pools (
			id text primary key,
			name text not null,
			auth_index text not null default '',
			model_prefix text not null default '',
			visibility text not null default 'anonymous',
			burst_mode text not null default '',
			burst_started_at integer not null default 0,
			burst_cycle_reset_at integer not null default 0,
			burst_snapshot_json text not null default '',
			sub2pool_account_id integer not null default 0,
			lend_during_burst integer not null default 0,
			hide_account integer not null default 0,
			routed_since integer not null default 0,
			provider text not null default '',
			budget_usd real,
			budget_reset_at integer not null default 0,
			created_at integer not null,
			updated_at integer not null
		)`,
		`create table if not exists pool_lend_windows (
			pool_id text not null,
			auth_index text not null,
			started_at integer not null,
			until_at integer not null,
			ended_at integer not null default 0,
			primary key(pool_id, started_at)
		)`,
		`create index if not exists idx_pool_lend_windows_auth on pool_lend_windows(auth_index, started_at)`,
		`create table if not exists pool_members (
			key_id text not null,
			pool_id text not null,
			share_percent real,
			role text not null default '',
			joined_at integer not null default 0,
			created_at integer not null,
			primary key(pool_id, key_id)
		)`,
		`create table if not exists pool_carry (
			pool_id text not null,
			key_id text not null,
			cycle_reset_at integer not null,
			pp real not null,
			source_cycle_reset_at integer not null,
			created_at integer not null,
			primary key(pool_id, key_id, cycle_reset_at)
		)`,
		`create table if not exists member_bursts (
			pool_id text not null,
			key_id text not null,
			cycle_reset_at integer not null,
			mode text not null,
			started_at integer not null,
			stopped_at integer not null default 0,
			primary key(pool_id, key_id, cycle_reset_at)
		)`,
		`create table if not exists pool_settlements (
			pool_id text not null,
			cycle_reset_at integer not null,
			started_at integer not null default 0,
			mode text not null,
			status text not null,
			detail_json text not null default '',
			created_at integer not null,
			primary key(pool_id, cycle_reset_at, started_at)
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureColumns(ctx, "usage_events", map[string]string{
		"billing_pending":        "integer not null default 0",
		"billing_pending_reason": "text not null default ''",
		"billing_price_model":    "text not null default ''",
		"requested_model":        "text default ''",
		"actual_model":           "text default ''",
		"provider":               "text default ''",
		"executor_type":          "text default ''",
		"ttft_ms":                "integer default 0",
		"reasoning_effort":       "text default ''",
		"service_tier":           "text default ''",
		"status_code":            "integer default 0",
		"auth_index":             "text not null default ''",
		"auth_id":                "text not null default ''",
		"consumed_at_ms":         "integer not null default 0",
	}); err != nil {
		return err
	}
	if err := s.ensureColumns(ctx, "pools", map[string]string{
		"burst_snapshot_json": "text not null default ''",
		"sub2pool_account_id": "integer not null default 0",
		"lend_during_burst":   "integer not null default 0",
		"hide_account":        "integer not null default 0",
		"routed_since":        "integer not null default 0",
		"provider":            "text not null default ''",
		"budget_usd":          "real",
		"budget_reset_at":     "integer not null default 0",
	}); err != nil {
		return err
	}
	if err := s.ensureColumns(ctx, "pool_members", map[string]string{
		"role":      "text not null default ''",
		"joined_at": "integer not null default 0",
	}); err != nil {
		return err
	}
	if err := s.migratePoolMembersKey(ctx); err != nil {
		return err
	}
	for _, stmt := range []string{
		`create index if not exists idx_usage_events_key_time on usage_events(key_id, requested_at)`,
		`create index if not exists idx_usage_events_auth_time on usage_events(auth_index, consumed_at_ms)`,
	} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureColumns(ctx, "keys", map[string]string{
		"billing_hold_reason":       "text not null default ''",
		"billing_hold_model":        "text not null default ''",
		"billing_hold_service_tier": "text not null default ''",
		"billing_hold_at":           "integer not null default 0",
		"weekly_only":               "integer not null default 0",
		"max_active_sessions":       "integer default 0",
		"archived":                  "integer not null default 0",
		"archived_at":               "integer not null default 0",
		"source":                    "text default ''",
		"source_present":            "integer not null default 1",
		"alias":                     "text default ''",
		"inherited_from":            "text default ''",
		"inherit_conflict":          "integer not null default 0",
		"hidden":                    "integer not null default 0",
		"last_enabled":              "integer not null default 0",
		"quota_unlimited":           "integer not null default 0",
	}); err != nil {
		return err
	}
	return s.migrateQuotaUnlimited(ctx)
}

// migratePoolMembersKey rekeys pool_members from key_id alone to
// (pool_id, key_id), so one key can ride pools of different providers.
func (s *Store) migratePoolMembersKey(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `pragma table_info(pool_members)`)
	if err != nil {
		return err
	}
	pk := map[string]int{}
	for rows.Next() {
		var cid, notNull, key int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &key); err != nil {
			rows.Close()
			return err
		}
		if key > 0 {
			pk[name] = key
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(pk) != 1 || pk["key_id"] == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`create table pool_members_v2 (
			key_id text not null,
			pool_id text not null,
			share_percent real,
			role text not null default '',
			joined_at integer not null default 0,
			created_at integer not null,
			primary key(pool_id, key_id)
		)`,
		`insert into pool_members_v2(key_id, pool_id, share_percent, role, joined_at, created_at)
			select key_id, pool_id, share_percent, role, joined_at, created_at from pool_members`,
		`drop table pool_members`,
		`alter table pool_members_v2 rename to pool_members`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ensureColumns(ctx context.Context, table string, columns map[string]string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`pragma table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, typ := range columns {
		if existing[name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`alter table %s add column %s %s`, table, name, typ)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertKey(ctx context.Context, key KeyRecord) error {
	if err := ValidateKeyRecord(key); err != nil {
		return err
	}
	if key.Source == "" {
		key.SourcePresent = true
	}
	if IsWeeklyOnlyQuota(key) {
		key.WeeklyOnly = true
	}
	models, _ := json.Marshal(key.Models)
	prices, _ := json.Marshal(key.Prices)
	_, err := s.db.ExecContext(
		ctx,
		`insert into keys(
			id, name, key_hash, enabled, preview, rpm, concurrency, max_active_sessions, models_json, prices_json,
			five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, weekly_only,
			archived, archived_at, source, source_present, alias, inherited_from, inherit_conflict, hidden, last_enabled, updated_at,
			quota_unlimited
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(id) do update set
			name=excluded.name,
			key_hash=excluded.key_hash,
			enabled=excluded.enabled,
			preview=excluded.preview,
			rpm=excluded.rpm,
			concurrency=excluded.concurrency,
			max_active_sessions=excluded.max_active_sessions,
			models_json=excluded.models_json,
			prices_json=excluded.prices_json,
			five_hour_limit_usd=case when coalesce(keys.weekly_only, 0) != 0 or excluded.weekly_only != 0 then null else coalesce(keys.five_hour_limit_usd, excluded.five_hour_limit_usd) end,
			daily_limit_usd=case when coalesce(keys.weekly_only, 0) != 0 or excluded.weekly_only != 0 then null else excluded.daily_limit_usd end,
			weekly_limit_usd=case when coalesce(keys.weekly_only, 0) != 0 then coalesce(keys.weekly_limit_usd, excluded.weekly_limit_usd) else excluded.weekly_limit_usd end,
			monthly_limit_usd=case when coalesce(keys.weekly_only, 0) != 0 or excluded.weekly_only != 0 then null else coalesce(keys.monthly_limit_usd, excluded.monthly_limit_usd) end,
			weekly_only=case when excluded.weekly_only != 0 then 1 else coalesce(keys.weekly_only, 0) end,
			archived=case when excluded.archived != 0 then excluded.archived else keys.archived end,
			archived_at=case when excluded.archived != 0 then excluded.archived_at else keys.archived_at end,
			source=case when excluded.source != '' then excluded.source else keys.source end,
			source_present=excluded.source_present,
			alias=excluded.alias,
			inherited_from=case when excluded.inherited_from != '' then excluded.inherited_from else keys.inherited_from end,
			inherit_conflict=excluded.inherit_conflict,
			hidden=excluded.hidden,
			last_enabled=case when excluded.last_enabled != 0 then excluded.last_enabled else keys.last_enabled end,
			quota_unlimited=case when excluded.quota_unlimited != 0 then 1 else coalesce(keys.quota_unlimited, 0) end,
			updated_at=excluded.updated_at`,
		key.ID,
		key.Name,
		key.KeyHash,
		boolInt(key.Enabled),
		key.Preview,
		key.RPM,
		key.Concurrency,
		key.MaxActiveSessions,
		string(models),
		string(prices),
		key.FiveHourUSD,
		key.DailyLimitUSD,
		key.WeeklyLimitUSD,
		key.MonthlyLimitUSD,
		boolInt(key.WeeklyOnly),
		boolInt(key.Archived),
		key.ArchivedAt,
		key.Source,
		boolInt(key.SourcePresent),
		key.Alias,
		key.InheritedFrom,
		boolInt(key.InheritConflict),
		boolInt(key.Hidden),
		boolInt(key.LastEnabled),
		time.Now().Unix(),
		boolInt(key.QuotaUnlimited),
	)
	return err
}

func (s *Store) ImportKeys(ctx context.Context, state KeyPolicyState) error {
	for _, key := range state.Keys {
		if err := s.UpsertKey(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ImportLegacyQuotaSQLite(ctx context.Context, path, source string) (LegacyImportResult, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return LegacyImportResult{}, nil
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return LegacyImportResult{}, err
	}
	defer db.Close()
	result := LegacyImportResult{}
	if ok, _ := legacyTableExists(ctx, db, "key_limits"); ok {
		n, err := s.importLegacyPortalLimits(ctx, db)
		if err != nil {
			return result, err
		}
		result.Limits += n
	}
	if ok, _ := legacyTableExists(ctx, db, "keys"); ok {
		n, err := s.importLegacyGovernorLimits(ctx, db)
		if err != nil {
			return result, err
		}
		result.Limits += n
	}
	if ok, _ := legacyTableExists(ctx, db, "reset_watermarks"); ok {
		n, err := s.importLegacyResets(ctx, db)
		if err != nil {
			return result, err
		}
		result.Resets += n
	}
	if result.Limits > 0 || result.Resets > 0 {
		_ = s.Audit(ctx, "system", "import_legacy_quota_sqlite", source, map[string]any{
			"path":   path,
			"limits": result.Limits,
			"resets": result.Resets,
		})
	}
	return result, nil
}

func legacyTableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(1) from sqlite_master where type='table' and name=?`, name).Scan(&count)
	return count > 0, err
}

func legacyColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`pragma table_info(%s)`, table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

func (s *Store) importLegacyPortalLimits(ctx context.Context, db *sql.DB) (int, error) {
	rows, err := db.QueryContext(ctx, `select policy_id, five_hour_limit_usd, monthly_limit_usd from key_limits`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		var fiveHour, monthly sql.NullFloat64
		if err := rows.Scan(&id, &fiveHour, &monthly); err != nil {
			return count, err
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, `update keys set
			five_hour_limit_usd=case when coalesce(weekly_only, 0) != 0 then null else coalesce(five_hour_limit_usd, ?) end,
			monthly_limit_usd=case when coalesce(weekly_only, 0) != 0 then null else coalesce(monthly_limit_usd, ?) end,
			updated_at=?
			where id=?`,
			nullFloatValue(fiveHour), nullFloatValue(monthly), time.Now().Unix(), id)
		if err != nil {
			return count, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

func (s *Store) importLegacyGovernorLimits(ctx context.Context, db *sql.DB) (int, error) {
	columns, err := legacyColumns(ctx, db, "keys")
	if err != nil {
		return 0, err
	}
	for _, required := range []string{"id", "five_hour_limit_usd", "daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd"} {
		if !columns[required] {
			return 0, nil
		}
	}
	rows, err := db.QueryContext(ctx, `select id, five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd from keys`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id string
		var fiveHour, daily, weekly, monthly sql.NullFloat64
		if err := rows.Scan(&id, &fiveHour, &daily, &weekly, &monthly); err != nil {
			return count, err
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		res, err := s.db.ExecContext(ctx, `update keys set
			five_hour_limit_usd=case when coalesce(weekly_only, 0) != 0 then null else coalesce(five_hour_limit_usd, ?) end,
			daily_limit_usd=case when coalesce(weekly_only, 0) != 0 then null else coalesce(daily_limit_usd, ?) end,
			weekly_limit_usd=coalesce(weekly_limit_usd, ?),
			monthly_limit_usd=case when coalesce(weekly_only, 0) != 0 then null else coalesce(monthly_limit_usd, ?) end,
			updated_at=?
			where id=?`,
			nullFloatValue(fiveHour), nullFloatValue(daily), nullFloatValue(weekly),
			nullFloatValue(monthly), time.Now().Unix(), id)
		if err != nil {
			return count, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

func (s *Store) importLegacyResets(ctx context.Context, db *sql.DB) (int, error) {
	columns, err := legacyColumns(ctx, db, "reset_watermarks")
	if err != nil {
		return 0, err
	}
	idColumn := ""
	for _, candidate := range []string{"key_id", "policy_id"} {
		if columns[candidate] {
			idColumn = candidate
			break
		}
	}
	timeColumn := ""
	for _, candidate := range []string{"reset_at", "reset_at_ms"} {
		if columns[candidate] {
			timeColumn = candidate
			break
		}
	}
	if idColumn == "" || timeColumn == "" || !columns["window"] {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`select %s, window, %s from reset_watermarks`, idColumn, timeColumn))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, window string
		var resetAt sql.NullInt64
		if err := rows.Scan(&id, &window, &resetAt); err != nil {
			return count, err
		}
		if strings.TrimSpace(id) == "" || strings.TrimSpace(window) == "" || !resetAt.Valid {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(window), Range7D) {
			weeklyOnly, err := s.isWeeklyOnlyKey(ctx, id)
			if err != nil {
				return count, err
			}
			if weeklyOnly {
				continue
			}
		}
		ts := resetAt.Int64
		if ts > 1_000_000_000_000 {
			ts = ts / 1000
		}
		res, err := s.db.ExecContext(ctx, `insert into reset_watermarks(key_id, window, reset_at) values(?, ?, ?)
			on conflict(key_id, window) do update set reset_at=excluded.reset_at
			where excluded.reset_at > reset_watermarks.reset_at`, id, window, ts)
		if err != nil {
			return count, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			count++
		}
	}
	return count, rows.Err()
}

func (s *Store) isWeeklyOnlyKey(ctx context.Context, id string) (bool, error) {
	var weeklyOnly int
	err := s.db.QueryRowContext(ctx, `select coalesce(weekly_only, 0) from keys where id=?`, id).Scan(&weeklyOnly)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return weeklyOnly != 0, err
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
	rows, err := s.db.QueryContext(ctx, `select id, name, key_hash, enabled, preview, rpm, concurrency, max_active_sessions, models_json, prices_json,
		five_hour_limit_usd, daily_limit_usd, weekly_limit_usd, monthly_limit_usd, archived, archived_at,
			coalesce(source, ''), coalesce(source_present, 1), coalesce(alias, ''), coalesce(inherited_from, ''),
		coalesce(inherit_conflict, 0), coalesce(hidden, 0), coalesce(last_enabled, enabled), coalesce(weekly_only, 0),
		coalesce(billing_hold_reason, ''), coalesce(billing_hold_model, ''), coalesce(billing_hold_service_tier, ''), coalesce(billing_hold_at, 0),
		coalesce(quota_unlimited, 0)
		from keys order by hidden asc, name collate nocase`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KeyRecord
	for rows.Next() {
		var key KeyRecord
		var enabled, archived, sourcePresent, inheritConflict, hidden, lastEnabled, weeklyOnly, unlimited int
		var modelsJSON, pricesJSON string
		var fiveHour, daily, weekly, monthly sql.NullFloat64
		if err := rows.Scan(
			&key.ID, &key.Name, &key.KeyHash, &enabled, &key.Preview, &key.RPM, &key.Concurrency, &key.MaxActiveSessions,
			&modelsJSON, &pricesJSON, &fiveHour, &daily, &weekly, &monthly,
			&archived, &key.ArchivedAt, &key.Source, &sourcePresent, &key.Alias, &key.InheritedFrom,
			&inheritConflict, &hidden, &lastEnabled, &weeklyOnly,
			&key.BillingHoldReason, &key.BillingHoldModel, &key.BillingHoldTier, &key.BillingHoldAt,
			&unlimited,
		); err != nil {
			return nil, err
		}
		key.Enabled = enabled != 0
		key.Archived = archived != 0
		key.SourcePresent = sourcePresent != 0
		key.InheritConflict = inheritConflict != 0
		key.Hidden = hidden != 0
		key.LastEnabled = lastEnabled != 0
		key.WeeklyOnly = weeklyOnly != 0
		key.QuotaUnlimited = unlimited != 0
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

func (s *Store) SetArchived(ctx context.Context, id string, archived bool, at time.Time) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("missing key id")
	}
	archivedAt := int64(0)
	if archived {
		archivedAt = at.Unix()
	}
	res, err := s.db.ExecContext(ctx, `update keys set archived=?, archived_at=?, updated_at=? where id=?`,
		boolInt(archived), archivedAt, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", id)
	}
	action := "archive_key"
	if !archived {
		action = "restore_key"
	}
	return s.Audit(ctx, "admin", action, id, map[string]any{"archived": archived, "archived_at": archivedAt})
}

func (s *Store) DeleteKey(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing key id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var name, preview string
	if err := tx.QueryRowContext(ctx, `select name, preview from keys where id=?`, id).Scan(&name, &preview); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("unknown key: %s", id)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from reset_watermarks where key_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from active_sessions where key_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from keys where id=?`, id); err != nil {
		return err
	}
	raw, _ := json.Marshal(map[string]any{"name": name, "preview": preview, "kept_usage_history": true})
	if _, err := tx.ExecContext(ctx, `insert into audit_log(timestamp, actor, action, target, detail_json) values(?, ?, ?, ?, ?)`,
		time.Now().Unix(), "admin", "delete_key", id, string(raw)); err != nil {
		return err
	}
	return tx.Commit()
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
	for _, limit := range []*float64{fiveHour, monthly} {
		if limit != nil && *limit < 0 {
			return fmt.Errorf("usd limits must not be negative")
		}
	}
	res, err := s.db.ExecContext(ctx, `update keys set
		five_hour_limit_usd=?,
		monthly_limit_usd=?,
		weekly_only=case when ? is not null or ? is not null then 0 else weekly_only end,
		updated_at=?
		where id=?`, fiveHour, monthly, fiveHour, monthly, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", id)
	}
	return s.Audit(ctx, "admin", "set_limits", id, map[string]any{"five_hour_usd": fiveHour, "monthly_usd": monthly})
}

// EnableWeeklyOnly is the safe migration operation for an existing policy.
// It changes no identity, allowlist, RPM, usage event, or reset watermark. An
// optional weekly limit fills only a previously unlimited key; it never
// overwrites the already-authoritative weekly budget.
func (s *Store) EnableWeeklyOnly(ctx context.Context, id string, weekly *float64) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("missing key id")
	}
	if weekly != nil && *weekly < 0 {
		return fmt.Errorf("usd limits must not be negative")
	}
	res, err := s.db.ExecContext(ctx, `update keys set
		five_hour_limit_usd=null,
		daily_limit_usd=null,
		weekly_limit_usd=coalesce(weekly_limit_usd, ?),
		monthly_limit_usd=null,
		weekly_only=1,
		updated_at=?
		where id=?`, weekly, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", id)
	}
	return s.Audit(ctx, "admin", "enable_weekly_only", id, map[string]any{
		"weekly_usd_fallback": weekly,
		"preserved":           []string{"identity", "rpm", "models", "usage_events", "reset_watermarks"},
	})
}

func (s *Store) SaveKeySettings(ctx context.Context, key KeyRecord) error {
	if err := ValidateKeyRecord(key); err != nil {
		return err
	}
	models, _ := json.Marshal(key.Models)
	prices, _ := json.Marshal(key.Prices)
	weeklyOnly := IsWeeklyOnlyQuota(key)
	res, err := s.db.ExecContext(ctx, `update keys set
		enabled=?,
		rpm=?,
		concurrency=?,
		max_active_sessions=?,
		models_json=?,
		prices_json=?,
		five_hour_limit_usd=?,
		daily_limit_usd=?,
		weekly_limit_usd=?,
		monthly_limit_usd=?,
		weekly_only=?,
		quota_unlimited=?,
		last_enabled=?,
		updated_at=?
		where id=?`,
		boolInt(key.Enabled),
		key.RPM,
		key.Concurrency,
		key.MaxActiveSessions,
		string(models),
		string(prices),
		key.FiveHourUSD,
		key.DailyLimitUSD,
		key.WeeklyLimitUSD,
		key.MonthlyLimitUSD,
		boolInt(weeklyOnly),
		boolInt(key.QuotaUnlimited),
		boolInt(key.Enabled),
		time.Now().Unix(),
		key.ID,
	)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return fmt.Errorf("unknown key: %s", key.ID)
	}
	return s.Audit(ctx, "admin", "save_key_settings", key.ID, map[string]any{
		"enabled":             key.Enabled,
		"rpm":                 key.RPM,
		"concurrency":         key.Concurrency,
		"max_active_sessions": key.MaxActiveSessions,
		"weekly_only":         weeklyOnly,
		"quota_unlimited":     key.QuotaUnlimited,
	})
}

func (s *Store) SyncNativeKeys(ctx context.Context, inputs []NativeKeySyncInput) error {
	existing, err := s.ListKeys(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]KeyRecord, len(existing))
	for _, key := range existing {
		byID[key.ID] = key
	}
	seen := map[string]bool{}
	now := time.Now().Unix()
	for _, input := range inputs {
		next, ok := NativeKeyRecord(input.RawKey, input.Alias)
		if !ok {
			continue
		}
		seen[next.ID] = true
		if current, exists := byID[next.ID]; exists {
			enabled := current.Enabled
			if isDefaultEmptyNativePolicy(current) {
				enabled = true
			}
			current.KeyHash = next.KeyHash
			current.Preview = next.Preview
			current.Source = NativeCPASource
			current.SourcePresent = true
			current.Hidden = false
			current.Alias = next.Alias
			current.Name = next.Name
			if _, err := s.db.ExecContext(ctx, `update keys set
				name=?, key_hash=?, preview=?, enabled=?, source=?, source_present=1, hidden=0, alias=?, updated_at=?
				where id=?`,
				current.Name, current.KeyHash, current.Preview, boolInt(enabled), current.Source, current.Alias, now, current.ID); err != nil {
				return err
			}
			continue
		}
		templates := policyTemplatesByAlias(existing, next.Alias)
		if len(templates) == 1 {
			next = CopyPolicyFields(next, templates[0])
			next.InheritedFrom = templates[0].ID
		} else if len(templates) > 1 {
			next.Enabled = false
			next.InheritConflict = true
		}
		if err := s.UpsertKey(ctx, next); err != nil {
			return err
		}
		if next.InheritedFrom != "" {
			_ = s.Audit(ctx, "system", "native_key_policy_inherited", next.ID, map[string]any{"from": next.InheritedFrom, "alias": next.Alias})
		}
	}
	for _, key := range existing {
		if key.Source != NativeCPASource || seen[key.ID] || !key.SourcePresent {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `update keys set enabled=0, source_present=0, hidden=1, last_enabled=?, updated_at=? where id=?`, boolInt(key.Enabled), now, key.ID); err != nil {
			return err
		}
		_ = s.Audit(ctx, "system", "native_key_source_removed", key.ID, map[string]any{"alias": key.Alias, "preview": key.Preview})
	}
	if err := s.retireInheritedLegacyTemplates(ctx, now); err != nil {
		return err
	}
	return nil
}

func isDefaultEmptyNativePolicy(key KeyRecord) bool {
	return key.Source == NativeCPASource &&
		key.SourcePresent &&
		!key.Enabled &&
		!key.LastEnabled &&
		key.RPM == 0 &&
		key.Concurrency == 0 &&
		key.MaxActiveSessions == 0 &&
		len(key.Models) == 0 &&
		len(key.Prices) == 0 &&
		key.FiveHourUSD == nil &&
		key.DailyLimitUSD == nil &&
		key.WeeklyLimitUSD == nil &&
		key.MonthlyLimitUSD == nil &&
		!key.InheritConflict
}

func policyTemplatesByAlias(keys []KeyRecord, alias string) []KeyRecord {
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return nil
	}
	var out []KeyRecord
	for _, key := range keys {
		if templateAlias(key) != alias {
			continue
		}
		if key.Source == NativeCPASource && !key.SourcePresent {
			out = append(out, key)
			continue
		}
		if key.Source == "" && key.SourcePresent && !key.Hidden {
			out = append(out, key)
		}
	}
	return out
}

func templateAlias(key KeyRecord) string {
	return strings.ToLower(strings.TrimSpace(firstNonEmpty(key.Alias, key.Name)))
}

func (s *Store) retireInheritedLegacyTemplates(ctx context.Context, now int64) error {
	rows, err := s.db.QueryContext(ctx, `select distinct inherited_from from keys where source=? and inherited_from != ''`, NativeCPASource)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.TrimSpace(id) == "" {
			continue
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `update keys set enabled=0, source=?, source_present=0, hidden=1, last_enabled=case when last_enabled != 0 then last_enabled else enabled end, updated_at=? where id=? and coalesce(source, '')=''`,
			LegacyPlusSource, now, id); err != nil {
			return err
		}
	}
	return nil
}

func LoadNativeKeysFromCPAConfig(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var body struct {
		APIKeys []string `yaml:"api-keys"`
	}
	if err := yaml.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.APIKeys))
	for _, key := range body.APIKeys {
		key = NormalizeSubmittedKey(key)
		if key == "" {
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

func LoadAPIKeyAliasesFromSQLite(ctx context.Context, path string) (map[string]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	exists, err := legacyTableExists(ctx, db, "api_key_aliases")
	if err != nil {
		return nil, err
	}
	if !exists {
		return map[string]string{}, nil
	}
	rows, err := db.QueryContext(ctx, `select api_key_hash, alias from api_key_aliases`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var hash, alias string
		if err := rows.Scan(&hash, &alias); err != nil {
			return nil, err
		}
		normalized, err := NormalizeHash(hash)
		if err != nil {
			continue
		}
		if alias = strings.TrimSpace(alias); alias != "" {
			out[normalized] = alias
		}
	}
	return out, rows.Err()
}

func LoadAPIKeyAliasesFromSQLitePaths(ctx context.Context, paths []string) (map[string]string, []string, error) {
	out := map[string]string{}
	var errs []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		aliases, err := LoadAPIKeyAliasesFromSQLite(ctx, path)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", path, Brief(err.Error(), 160)))
			continue
		}
		for hash, alias := range aliases {
			if strings.TrimSpace(alias) == "" {
				continue
			}
			out[hash] = alias
		}
	}
	if len(errs) > 0 && len(out) == 0 {
		return out, errs, fmt.Errorf(strings.Join(errs, "; "))
	}
	return out, errs, nil
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
	if event.BillingPending {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if err := insertUsage(ctx, tx, event); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `update keys set
			billing_hold_reason=?, billing_hold_model=?, billing_hold_service_tier=?, billing_hold_at=?, updated_at=?
			where id=?`,
			event.BillingReason, event.BillingModel, event.ServiceTier, time.Now().Unix(), time.Now().Unix(), event.KeyID)
		if err != nil {
			return err
		}
		if affected, _ := res.RowsAffected(); affected == 0 {
			return fmt.Errorf("unknown key: %s", event.KeyID)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = s.Audit(ctx, "system", "billing_hold_set", event.KeyID, map[string]any{
			"model": event.BillingModel, "service_tier": event.ServiceTier, "reason": event.BillingReason, "request_id": event.RequestID,
		})
		return nil
	}
	return insertUsage(ctx, s.db, event)
}

func insertUsage(ctx context.Context, execer sqlExecer, event UsageEvent) error {
	breakdown, _ := json.Marshal(event.CostBreakdown)
	_, err := execer.ExecContext(
		ctx,
		`insert into usage_events(
			request_id, key_id, key_preview, model, requested_model, actual_model, provider, executor_type,
			endpoint, requested_at, latency_ms, ttft_ms, reasoning_effort, service_tier, status_code, failed, failure,
			input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
			reasoning_tokens, total_tokens, cost, cost_breakdown_json, billing_pending, billing_pending_reason, billing_price_model,
			auth_index, auth_id, consumed_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.RequestID, event.KeyID, event.KeyPreview, event.Model, event.RequestedModel, event.ActualModel,
		event.Provider, event.ExecutorType, event.Endpoint, event.RequestedAt.Unix(),
		event.LatencyMS, event.TTFTMS, event.ReasoningEffort, event.ServiceTier, event.StatusCode,
		boolInt(event.Failed), event.Failure,
		event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.CachedTokens, event.Usage.CacheReadTokens,
		event.Usage.CacheCreationTokens, event.Usage.ReasoningTokens, event.Usage.TotalTokens,
		event.Cost, string(breakdown), boolInt(event.BillingPending), event.BillingReason, event.BillingModel,
		strings.TrimSpace(event.AuthIndex), strings.TrimSpace(event.AuthID), consumedAtMS(event),
	)
	return err
}

func consumedAtMS(event UsageEvent) int64 {
	if event.ConsumedAtMS > 0 {
		return event.ConsumedAtMS
	}
	return event.RequestedAt.UnixMilli() + max(event.LatencyMS, 0)
}

// ResolvePendingBilling only touches rows explicitly marked billing_pending.
// It never recalculates ordinary history, and clears the durable key hold only
// after every pending row has a currently verified CPAMP price.
func (s *Store) ResolvePendingBilling(ctx context.Context, keyID string, book PriceBook) (int, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return 0, fmt.Errorf("missing key id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `select id, coalesce(billing_price_model, ''), coalesce(actual_model, ''),
		coalesce(requested_model, ''), coalesce(model, ''), coalesce(service_tier, ''),
		coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(cached_tokens, 0), coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(total_tokens, 0)
		from usage_events where key_id=? and coalesce(billing_pending, 0) != 0 order by id`, keyID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type update struct {
		id        int64
		model     string
		cost      float64
		breakdown CostBreakdown
	}
	updates := []update{}
	for rows.Next() {
		var item update
		var actualModel, requestedModel, visibleModel, serviceTier string
		var usage TokenUsage
		if err := rows.Scan(
			&item.id, &item.model, &actualModel, &requestedModel, &visibleModel, &serviceTier,
			&usage.InputTokens, &usage.OutputTokens, &usage.CachedTokens, &usage.CacheReadTokens,
			&usage.CacheCreationTokens, &usage.ReasoningTokens, &usage.TotalTokens,
		); err != nil {
			return 0, err
		}
		item.model = firstNonEmpty(item.model, actualModel, requestedModel, visibleModel)
		breakdown, priced := CostForUsageFromPriceBook(book, usage, item.model, serviceTier)
		if !priced {
			return 0, fmt.Errorf("verified CPAMP price is still unavailable for pending model %s", item.model)
		}
		item.cost = breakdown.Costs["total"]
		item.breakdown = breakdown
		updates = append(updates, item)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(updates) == 0 {
		return 0, fmt.Errorf("no pending billing events for key: %s", keyID)
	}
	stmt, err := tx.PrepareContext(ctx, `update usage_events set
		cost=?, cost_breakdown_json=?, billing_pending=0, billing_pending_reason='', billing_price_model=?
		where id=? and coalesce(billing_pending, 0) != 0`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, item := range updates {
		raw, _ := json.Marshal(item.breakdown)
		res, err := stmt.ExecContext(ctx, item.cost, string(raw), item.model, item.id)
		if err != nil {
			return 0, err
		}
		if affected, _ := res.RowsAffected(); affected != 1 {
			return 0, fmt.Errorf("pending billing changed during resolution for event %d", item.id)
		}
	}
	res, err := tx.ExecContext(ctx, `update keys set
		billing_hold_reason='', billing_hold_model='', billing_hold_service_tier='', billing_hold_at=0, updated_at=?
		where id=? and not exists (
			select 1 from usage_events where key_id=? and coalesce(billing_pending, 0) != 0
		)`, time.Now().Unix(), keyID, keyID)
	if err != nil {
		return 0, err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		var exists int
		err := tx.QueryRowContext(ctx, `select count(1) from keys where id=?`, keyID).Scan(&exists)
		if err != nil {
			return 0, err
		}
		if exists == 0 {
			return 0, fmt.Errorf("unknown key: %s", keyID)
		}
		return 0, fmt.Errorf("pending billing changed during resolution for key: %s", keyID)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	_ = s.Audit(ctx, "admin", "billing_hold_resolved", keyID, map[string]any{"events": len(updates), "price_source": book.NormalizedSource()})
	return len(updates), nil
}

func (s *Store) RecalculateUsageCosts(ctx context.Context, since time.Time, book PriceBook) (int, error) {
	if s == nil || s.db == nil || len(book.Prices) == 0 {
		return 0, nil
	}
	rows, err := s.db.QueryContext(ctx, `select id, coalesce(model, ''), coalesce(requested_model, ''), coalesce(service_tier, ''),
		coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(cached_tokens, 0), coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(total_tokens, 0)
		from usage_events where requested_at >= ? and coalesce(billing_pending, 0) = 0`, since.Unix())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type update struct {
		id        int64
		cost      float64
		breakdown CostBreakdown
	}
	var updates []update
	for rows.Next() {
		var id int64
		var model, requestedModel, serviceTier string
		var usage TokenUsage
		if err := rows.Scan(
			&id, &model, &requestedModel, &serviceTier,
			&usage.InputTokens, &usage.OutputTokens, &usage.CachedTokens, &usage.CacheReadTokens,
			&usage.CacheCreationTokens, &usage.ReasoningTokens, &usage.TotalTokens,
		); err != nil {
			return 0, err
		}
		visibleModel := strings.TrimSpace(requestedModel)
		if visibleModel == "" {
			visibleModel = strings.TrimSpace(model)
		}
		breakdown, _ := CostForUsageFromPriceBook(book, usage, visibleModel, serviceTier)
		updates = append(updates, update{id: id, cost: breakdown.Costs["total"], breakdown: breakdown})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(updates) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	stmt, err := tx.PrepareContext(ctx, `update usage_events set cost=?, cost_breakdown_json=? where id=?`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, item := range updates {
		raw, _ := json.Marshal(item.breakdown)
		if _, err := stmt.ExecContext(ctx, item.cost, string(raw), item.id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(updates), nil
}

// RequestRate is a key's request pace: requests in the last 60 seconds and
// the most seen in any 60 seconds since the queried start.
type RequestRate struct {
	LastMinute int `json:"last_minute"`
	Peak       int `json:"peak"`
}

func (s *Store) RequestRate(ctx context.Context, keyID string, since, now time.Time) (RequestRate, error) {
	rows, err := s.db.QueryContext(ctx, `select requested_at from usage_events
		where key_id = ? and requested_at >= ? and requested_at <= ? order by requested_at`, keyID, since.Unix(), now.Unix())
	if err != nil {
		return RequestRate{}, err
	}
	defer rows.Close()
	var out RequestRate
	var times []int64
	start := 0
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return RequestRate{}, err
		}
		times = append(times, at)
		for times[start] <= at-60 {
			start++
		}
		out.Peak = max(out.Peak, len(times)-start)
	}
	for i := len(times) - 1; i >= 0 && times[i] > now.Unix()-60; i-- {
		out.LastMinute++
	}
	return out, rows.Err()
}

func (s *Store) UsageSum(ctx context.Context, keyID string, window Window) (float64, error) {
	var total sql.NullFloat64
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	args := []any{from, window.To.Unix()}
	query := `select coalesce(sum(cost), 0) from usage_events where requested_at >= ? and requested_at <= ? and coalesce(billing_pending, 0) = 0`
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

// UsageSumMatching is UsageSum over the models keep accepts; a nil keep
// counts every model.
func (s *Store) UsageSumMatching(ctx context.Context, keyID string, window Window, keep func(model string) bool) (float64, error) {
	if keep == nil {
		return s.UsageSum(ctx, keyID, window)
	}
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	rows, err := s.db.QueryContext(ctx, `select coalesce(model, ''), coalesce(sum(cost), 0) from usage_events
		where key_id = ? and requested_at >= ? and requested_at <= ? and coalesce(billing_pending, 0) = 0
		group by coalesce(model, '')`, keyID, from, window.To.Unix())
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	total := 0.0
	for rows.Next() {
		var model string
		var cost float64
		if err := rows.Scan(&model, &cost); err != nil {
			return 0, err
		}
		if keep(model) {
			total += cost
		}
	}
	return total, rows.Err()
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

func (s *Store) UsageSummary(ctx context.Context, keyID string, window Window) (UsageSummary, error) {
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	args := []any{from, window.To.Unix()}
	query := `select
		count(*),
		coalesce(sum(case when failed != 0 then 1 else 0 end), 0),
		coalesce(sum(case when billing_pending != 0 then 1 else 0 end), 0),
		coalesce(sum(case when billing_pending = 0 then cost else 0 end), 0),
		coalesce(sum(input_tokens), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(cached_tokens), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(total_tokens), 0)
		from usage_events where requested_at >= ? and requested_at <= ?`
	if keyID != "" && keyID != "all" {
		query += ` and key_id = ?`
		args = append(args, keyID)
	}
	var summary UsageSummary
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&summary.Calls,
		&summary.Failed,
		&summary.BillingPending,
		&summary.TotalCost,
		&summary.Usage.InputTokens,
		&summary.Usage.OutputTokens,
		&summary.Usage.CachedTokens,
		&summary.Usage.CacheReadTokens,
		&summary.Usage.CacheCreationTokens,
		&summary.Usage.ReasoningTokens,
		&summary.Usage.TotalTokens,
	); err != nil {
		return UsageSummary{}, err
	}
	return summary, nil
}

// UsageBucket is one slice of a key's spend timeline. Cost skips
// billing_pending rows exactly like UsageSum, so the buckets of a window add up
// to the amount the quota check sees.
type UsageBucket struct {
	Start          int64   `json:"start"`
	End            int64   `json:"end"`
	Calls          int64   `json:"calls"`
	Failed         int64   `json:"failed"`
	BillingPending int64   `json:"billing_pending"`
	Cost           float64 `json:"cost"`
}

type UsageSeries struct {
	Range         string        `json:"range"`
	From          int64         `json:"from"`
	To            int64         `json:"to"`
	ResetAt       int64         `json:"reset_at,omitempty"`
	BucketSeconds int64         `json:"bucket_seconds"`
	TotalCost     float64       `json:"total_cost"`
	Buckets       []UsageBucket `json:"buckets"`
}

// UsageSeries buckets one key's usage inside window. Bucket edges follow the
// viewer's local clock through offsetSeconds (east of UTC is positive). The
// first bucket starts at the window start and the last one ends at the window
// end, so a partial hour or day at either edge stays in the series.
func (s *Store) UsageSeries(ctx context.Context, keyID string, window Window, bucket time.Duration, offsetSeconds int64) (UsageSeries, error) {
	size := int64(bucket / time.Second)
	if size <= 0 {
		return UsageSeries{}, fmt.Errorf("bucket must be at least one second")
	}
	if keyID == "" || keyID == "all" {
		return UsageSeries{}, fmt.Errorf("usage series needs a single key")
	}
	from := window.From.Unix()
	to := window.To.Unix()
	out := UsageSeries{Range: window.Name, From: from, To: to, BucketSeconds: size, Buckets: []UsageBucket{}}
	if to < from {
		return out, nil
	}
	counted := from
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		out.ResetAt = resetAt
		counted = resetAt
	}
	align := func(ts int64) int64 {
		shifted := ts + offsetSeconds
		return shifted - ((shifted%size)+size)%size - offsetSeconds
	}
	first := align(from)
	for start := first; start <= to; start += size {
		out.Buckets = append(out.Buckets, UsageBucket{Start: max(start, from), End: min(start+size, to)})
	}
	rows, err := s.db.QueryContext(ctx, `select requested_at, coalesce(failed, 0), coalesce(billing_pending, 0), coalesce(cost, 0)
		from usage_events where key_id = ? and requested_at >= ? and requested_at <= ?`, keyID, counted, to)
	if err != nil {
		return UsageSeries{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		var failed, pending int
		var cost float64
		if err := rows.Scan(&ts, &failed, &pending, &cost); err != nil {
			return UsageSeries{}, err
		}
		idx := int((align(ts) - first) / size)
		if idx < 0 || idx >= len(out.Buckets) {
			continue
		}
		item := &out.Buckets[idx]
		item.Calls++
		if failed != 0 {
			item.Failed++
		}
		if pending != 0 {
			item.BillingPending++
			continue
		}
		item.Cost += cost
		out.TotalCost += cost
	}
	return out, rows.Err()
}

// WindowRecoveryAt answers when an exhausted rolling window falls back under
// limit if no new usage arrives. The quota check blocks at used >= limit, so
// recovery is the moment enough of the oldest settled spend leaves the window.
// ok is false when the window is not exhausted, does not roll, or cannot
// recover through aging alone (a zero limit).
func (s *Store) WindowRecoveryAt(ctx context.Context, keyID string, window Window, limit float64) (time.Time, bool, error) {
	return s.WindowRecoveryAtMatching(ctx, keyID, window, limit, nil)
}

// WindowRecoveryAtMatching is WindowRecoveryAt over the models keep accepts;
// a nil keep counts every model.
func (s *Store) WindowRecoveryAtMatching(ctx context.Context, keyID string, window Window, limit float64, keep func(model string) bool) (time.Time, bool, error) {
	duration := RollingWindowDuration(window.Name)
	if duration <= 0 || keyID == "" || keyID == "all" || limit <= 0 {
		return time.Time{}, false, nil
	}
	from := window.From.Unix()
	if resetAt, ok := s.ResetAt(ctx, keyID, window.Name); ok && resetAt > from {
		from = resetAt
	}
	rows, err := s.db.QueryContext(ctx, `select requested_at, coalesce(cost, 0), coalesce(model, '') from usage_events
		where key_id = ? and requested_at >= ? and requested_at <= ? and coalesce(billing_pending, 0) = 0
		order by requested_at asc, id asc`, keyID, from, window.To.Unix())
	if err != nil {
		return time.Time{}, false, err
	}
	defer rows.Close()
	type spend struct {
		ts   int64
		cost float64
	}
	var items []spend
	total := 0.0
	for rows.Next() {
		var item spend
		var model string
		if err := rows.Scan(&item.ts, &item.cost, &model); err != nil {
			return time.Time{}, false, err
		}
		if keep != nil && !keep(model) {
			continue
		}
		items = append(items, item)
		total += item.cost
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, false, err
	}
	if total < limit {
		return time.Time{}, false, nil
	}
	remaining := total
	for _, item := range items {
		remaining -= item.cost
		if remaining < limit {
			// UsageSum keeps rows with requested_at >= now-duration, so a row
			// stops counting one second after it is exactly duration old.
			return time.Unix(item.ts, 0).Add(duration + time.Second), true, nil
		}
	}
	return time.Time{}, false, nil
}

func (s *Store) RecentEvents(ctx context.Context, keyID string, limit int) ([]UsageEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select coalesce(request_id, ''), coalesce(key_id, ''), coalesce(key_preview, ''), coalesce(model, ''),
		coalesce(requested_model, ''), coalesce(actual_model, ''), coalesce(provider, ''), coalesce(executor_type, ''),
		coalesce(endpoint, ''), requested_at, coalesce(latency_ms, 0), coalesce(ttft_ms, 0),
		coalesce(reasoning_effort, ''), coalesce(service_tier, ''), coalesce(status_code, 0), coalesce(failed, 0), coalesce(failure, ''),
		coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(cached_tokens, 0), coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(total_tokens, 0), coalesce(cost, 0), coalesce(cost_breakdown_json, '{}'),
		coalesce(billing_pending, 0), coalesce(billing_pending_reason, ''), coalesce(billing_price_model, '')
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
		var billingPending int
		var breakdown string
		if err := rows.Scan(
			&event.RequestID, &event.KeyID, &event.KeyPreview, &event.Model, &event.RequestedModel, &event.ActualModel,
			&event.Provider, &event.ExecutorType, &event.Endpoint, &ts, &event.LatencyMS, &event.TTFTMS,
			&event.ReasoningEffort, &event.ServiceTier, &event.StatusCode, &failed, &event.Failure,
			&event.Usage.InputTokens, &event.Usage.OutputTokens, &event.Usage.CachedTokens, &event.Usage.CacheReadTokens,
			&event.Usage.CacheCreationTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens, &event.Cost, &breakdown,
			&billingPending, &event.BillingReason, &event.BillingModel,
		); err != nil {
			return nil, err
		}
		event.RequestedAt = time.Unix(ts, 0)
		event.Failed = failed != 0
		event.BillingPending = billingPending != 0
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
	query := `select coalesce(request_id, ''), coalesce(key_id, ''), coalesce(key_preview, ''), coalesce(model, ''),
		coalesce(requested_model, ''), coalesce(actual_model, ''), coalesce(provider, ''), coalesce(executor_type, ''),
		coalesce(endpoint, ''), requested_at, coalesce(latency_ms, 0), coalesce(ttft_ms, 0),
		coalesce(reasoning_effort, ''), coalesce(service_tier, ''), coalesce(status_code, 0), coalesce(failed, 0), coalesce(failure, ''),
		coalesce(input_tokens, 0), coalesce(output_tokens, 0), coalesce(cached_tokens, 0), coalesce(cache_read_tokens, 0),
		coalesce(cache_creation_tokens, 0), coalesce(reasoning_tokens, 0), coalesce(total_tokens, 0), coalesce(cost, 0), coalesce(cost_breakdown_json, '{}'),
		coalesce(billing_pending, 0), coalesce(billing_pending_reason, ''), coalesce(billing_price_model, '')
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
		var billingPending int
		var breakdown string
		if err := rows.Scan(
			&event.RequestID, &event.KeyID, &event.KeyPreview, &event.Model, &event.RequestedModel, &event.ActualModel,
			&event.Provider, &event.ExecutorType, &event.Endpoint, &ts, &event.LatencyMS, &event.TTFTMS,
			&event.ReasoningEffort, &event.ServiceTier, &event.StatusCode, &failed, &event.Failure,
			&event.Usage.InputTokens, &event.Usage.OutputTokens, &event.Usage.CachedTokens, &event.Usage.CacheReadTokens,
			&event.Usage.CacheCreationTokens, &event.Usage.ReasoningTokens, &event.Usage.TotalTokens, &event.Cost, &breakdown,
			&billingPending, &event.BillingReason, &event.BillingModel,
		); err != nil {
			return nil, err
		}
		event.RequestedAt = time.Unix(ts, 0)
		event.Failed = failed != 0
		event.BillingPending = billingPending != 0
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

func (s *Store) RecentCodexSummaries(ctx context.Context, keyID string, limit int) ([]CodexSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select request_id, key_id, model, protection, summary_json, updated_at from codexcont_summaries`
	args := []any{}
	if keyID != "" && keyID != "all" {
		query += ` where key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by updated_at desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CodexSummary
	for rows.Next() {
		var item CodexSummary
		var raw string
		var ts int64
		if err := rows.Scan(&item.RequestID, &item.KeyID, &item.Model, &item.Protection, &raw, &ts); err != nil {
			return nil, err
		}
		item.UpdatedAt = time.Unix(ts, 0)
		_ = json.Unmarshal([]byte(raw), &item.Summary)
		if item.Summary == nil {
			item.Summary = map[string]any{}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func RecentCodexSummariesFromSQLite(ctx context.Context, path, keyID string, limit int) ([]CodexSummary, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select request_id, key_id, model, protection, summary_json, updated_at from codexcont_summaries`
	args := []any{}
	if keyID != "" && keyID != "all" {
		query += ` where key_id = ?`
		args = append(args, keyID)
	}
	query += ` order by updated_at desc limit ?`
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CodexSummary
	for rows.Next() {
		var item CodexSummary
		var raw string
		var ts int64
		if err := rows.Scan(&item.RequestID, &item.KeyID, &item.Model, &item.Protection, &raw, &ts); err != nil {
			return nil, err
		}
		item.UpdatedAt = time.Unix(ts, 0)
		_ = json.Unmarshal([]byte(raw), &item.Summary)
		if item.Summary == nil {
			item.Summary = map[string]any{}
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) Audit(ctx context.Context, actor, action, target string, detail any) error {
	raw, _ := json.Marshal(detail)
	_, err := s.db.ExecContext(ctx, `insert into audit_log(timestamp, actor, action, target, detail_json) values(?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, target, string(raw))
	return err
}

func (s *Store) AuditCount(ctx context.Context, action string) (int, error) {
	var count int
	args := []any{}
	query := `select count(1) from audit_log`
	if strings.TrimSpace(action) != "" {
		query += ` where action = ?`
		args = append(args, action)
	}
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&count)
	return count, err
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
	return s.Audit(ctx, "admin", "set_settings", "policyplus", values)
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

func nullFloatValue(value sql.NullFloat64) any {
	if !value.Valid {
		return nil
	}
	return value.Float64
}

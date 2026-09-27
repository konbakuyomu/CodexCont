package policyplus

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const cpampPriceBookCacheSetting = "cpamp_price_book_cache"

func LoadCPAMPPriceBookFromSQLite(ctx context.Context, path string) (PriceBook, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return PriceBook{}, fmt.Errorf("empty CPAMP price DB path")
	}
	if _, err := os.Stat(path); err != nil {
		return PriceBook{}, err
	}
	db, err := openSQLite(path, true)
	if err != nil {
		return PriceBook{}, err
	}
	defer db.Close()
	exists, err := legacyTableExists(ctx, db, "model_prices")
	if err != nil {
		return PriceBook{}, err
	}
	if !exists {
		return PriceBook{}, fmt.Errorf("model_prices table not found")
	}
	columns, err := legacyColumns(ctx, db, "model_prices")
	if err != nil {
		return PriceBook{}, err
	}
	for _, column := range []string{"model", "prompt_per_1m", "completion_per_1m", "cache_per_1m"} {
		if !columns[column] {
			return PriceBook{}, fmt.Errorf("model_prices.%s column not found", column)
		}
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`select
		model, coalesce(prompt_per_1m, 0), coalesce(completion_per_1m, 0), coalesce(cache_per_1m, 0), %s, %s,
		%s, %s, %s, %s, %s, %s, %s, %s
		from model_prices order by model`,
		cpampColumn(columns, "cache_read_per_1m", "0"),
		cpampColumn(columns, "cache_creation_per_1m", "0"),
		cpampColumn(columns, "source", "''"),
		cpampColumn(columns, "source_model_id", "''"),
		cpampColumn(columns, "updated_at_ms", "0"),
		cpampColumn(columns, "synced_at_ms", "0"),
		cpampColumn(columns, "prompt_configured", "0"),
		cpampColumn(columns, "completion_configured", "0"),
		cpampColumn(columns, "cache_read_configured", "0"),
		cpampColumn(columns, "cache_creation_configured", "0"),
	))
	if err != nil {
		return PriceBook{}, err
	}
	defer rows.Close()
	book := PriceBook{
		Source:   CostSourceCPAMPPriceBook,
		Path:     path,
		LoadedAt: time.Now().UTC(),
		Prices:   map[string]ModelPrice{},
	}
	for rows.Next() {
		var model, source, sourceModel string
		var updatedAt, syncedAt sql.NullInt64
		var inputConfigured, outputConfigured, cacheReadConfigured, cacheCreationConfigured int
		price := ModelPrice{}
		if err := rows.Scan(
			&model,
			&price.InputPerMillion,
			&price.OutputPerMillion,
			&price.CachePerMillion,
			&price.CacheReadPerMillion,
			&price.CacheCreationPerMillion,
			&source,
			&sourceModel,
			&updatedAt,
			&syncedAt,
			&inputConfigured,
			&outputConfigured,
			&cacheReadConfigured,
			&cacheCreationConfigured,
		); err != nil {
			return PriceBook{}, err
		}
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		price.Model = model
		price.Provider = source
		price.TargetModel = sourceModel
		price.InputConfigured = configuredOrPositive(inputConfigured, price.InputPerMillion)
		price.OutputConfigured = configuredOrPositive(outputConfigured, price.OutputPerMillion)
		price.CacheReadConfigured = configuredOrPositive(cacheReadConfigured, price.CacheReadPerMillion)
		price.CacheCreationConfigured = configuredOrPositive(cacheCreationConfigured, price.CacheCreationPerMillion)
		book.Prices[model] = price
		if updatedAt.Valid && updatedAt.Int64 > book.UpdatedAtMS {
			book.UpdatedAtMS = updatedAt.Int64
		}
		if syncedAt.Valid && syncedAt.Int64 > book.SyncedAtMS {
			book.SyncedAtMS = syncedAt.Int64
		}
	}
	if err := rows.Err(); err != nil {
		return PriceBook{}, err
	}
	if err := loadCPAMPContextTiers(ctx, db, &book); err != nil {
		return PriceBook{}, err
	}
	if err := loadCPAMPServiceTiers(ctx, db, &book); err != nil {
		return PriceBook{}, err
	}
	return book, nil
}

func cpampColumn(columns map[string]bool, name, fallback string) string {
	if columns[name] {
		return fmt.Sprintf("coalesce(%s, %s)", name, fallback)
	}
	return fallback
}

func configuredOrPositive(configured int, value float64) bool {
	return configured != 0 || value > 0
}

func loadCPAMPContextTiers(ctx context.Context, db *sql.DB, book *PriceBook) error {
	exists, err := legacyTableExists(ctx, db, "model_price_context_tiers")
	if err != nil || !exists {
		return err
	}
	columns, err := legacyColumns(ctx, db, "model_price_context_tiers")
	if err != nil {
		return err
	}
	for _, column := range []string{"model", "threshold_tokens", "prompt_per_1m", "completion_per_1m", "cache_per_1m"} {
		if !columns[column] {
			return fmt.Errorf("model_price_context_tiers.%s column not found", column)
		}
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`select
		model, threshold_tokens, coalesce(prompt_per_1m, 0), coalesce(completion_per_1m, 0), coalesce(cache_per_1m, 0), %s, %s,
		%s, %s, %s, %s, %s
		from model_price_context_tiers order by model, threshold_tokens`,
		cpampColumn(columns, "cache_read_per_1m", "0"),
		cpampColumn(columns, "cache_creation_per_1m", "0"),
		cpampColumn(columns, "prompt_configured", "0"),
		cpampColumn(columns, "completion_configured", "0"),
		cpampColumn(columns, "cache_configured", "0"),
		cpampColumn(columns, "cache_read_configured", "0"),
		cpampColumn(columns, "cache_creation_configured", "0"),
	))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var tier ModelPriceContextTier
		var inputConfigured, outputConfigured, cacheConfigured, cacheReadConfigured, cacheCreationConfigured int
		if err := rows.Scan(
			&model, &tier.ThresholdTokens, &tier.InputPerMillion, &tier.OutputPerMillion, &tier.CachePerMillion,
			&tier.CacheReadPerMillion, &tier.CacheCreationPerMillion,
			&inputConfigured, &outputConfigured, &cacheConfigured, &cacheReadConfigured, &cacheCreationConfigured,
		); err != nil {
			return err
		}
		model = strings.TrimSpace(model)
		price, ok := book.Prices[model]
		if !ok || tier.ThresholdTokens <= 0 {
			continue
		}
		tier.InputConfigured = configuredOrPositive(inputConfigured, tier.InputPerMillion)
		tier.OutputConfigured = configuredOrPositive(outputConfigured, tier.OutputPerMillion)
		tier.CacheConfigured = configuredOrPositive(cacheConfigured, tier.CachePerMillion)
		tier.CacheReadConfigured = configuredOrPositive(cacheReadConfigured, tier.CacheReadPerMillion)
		tier.CacheCreationConfigured = configuredOrPositive(cacheCreationConfigured, tier.CacheCreationPerMillion)
		price.ContextTiers = append(price.ContextTiers, tier)
		book.Prices[model] = price
	}
	return rows.Err()
}

func loadCPAMPServiceTiers(ctx context.Context, db *sql.DB, book *PriceBook) error {
	exists, err := legacyTableExists(ctx, db, "model_price_service_tiers")
	if err != nil || !exists {
		return err
	}
	columns, err := legacyColumns(ctx, db, "model_price_service_tiers")
	if err != nil {
		return err
	}
	for _, column := range []string{"model", "mode", "service_tier", "prompt_per_1m", "completion_per_1m", "cache_per_1m"} {
		if !columns[column] {
			return fmt.Errorf("model_price_service_tiers.%s column not found", column)
		}
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`select
		model, mode, service_tier, coalesce(prompt_per_1m, 0), coalesce(completion_per_1m, 0), coalesce(cache_per_1m, 0), %s, %s,
		%s, %s, %s, %s, %s
		from model_price_service_tiers order by model, mode, service_tier`,
		cpampColumn(columns, "cache_read_per_1m", "0"),
		cpampColumn(columns, "cache_creation_per_1m", "0"),
		cpampColumn(columns, "prompt_configured", "0"),
		cpampColumn(columns, "completion_configured", "0"),
		cpampColumn(columns, "cache_configured", "0"),
		cpampColumn(columns, "cache_read_configured", "0"),
		cpampColumn(columns, "cache_creation_configured", "0"),
	))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model string
		var tier ModelPriceServiceTier
		var inputConfigured, outputConfigured, cacheConfigured, cacheReadConfigured, cacheCreationConfigured int
		if err := rows.Scan(
			&model, &tier.Mode, &tier.ServiceTier, &tier.InputPerMillion, &tier.OutputPerMillion, &tier.CachePerMillion,
			&tier.CacheReadPerMillion, &tier.CacheCreationPerMillion,
			&inputConfigured, &outputConfigured, &cacheConfigured, &cacheReadConfigured, &cacheCreationConfigured,
		); err != nil {
			return err
		}
		model = strings.TrimSpace(model)
		price, ok := book.Prices[model]
		if !ok || strings.TrimSpace(tier.Mode) == "" || strings.TrimSpace(tier.ServiceTier) == "" {
			continue
		}
		tier.InputConfigured = configuredOrPositive(inputConfigured, tier.InputPerMillion)
		tier.OutputConfigured = configuredOrPositive(outputConfigured, tier.OutputPerMillion)
		tier.CacheConfigured = configuredOrPositive(cacheConfigured, tier.CachePerMillion)
		tier.CacheReadConfigured = configuredOrPositive(cacheReadConfigured, tier.CacheReadPerMillion)
		tier.CacheCreationConfigured = configuredOrPositive(cacheCreationConfigured, tier.CacheCreationPerMillion)
		price.ServiceTiers = append(price.ServiceTiers, tier)
		book.Prices[model] = price
	}
	return rows.Err()
}

func LoadCPAMPPriceBookFromSQLitePaths(ctx context.Context, paths []string) (PriceBook, []string, error) {
	var warnings []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		book, err := LoadCPAMPPriceBookFromSQLite(ctx, path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %s", path, Brief(err.Error(), 160)))
			continue
		}
		if len(book.Prices) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: model_prices 为空", path))
			continue
		}
		book.Warnings = warnings
		return book, warnings, nil
	}
	if len(paths) == 0 {
		return PriceBook{}, warnings, fmt.Errorf("no CPAMP price DB path configured")
	}
	if len(warnings) == 0 {
		warnings = append(warnings, "没有可用的 CPAMP model_prices 数据库")
	}
	return PriceBook{}, warnings, errors.New(strings.Join(warnings, "; "))
}

func (s *Store) SaveCachedPriceBook(ctx context.Context, book PriceBook) error {
	if s == nil || s.db == nil || len(book.Prices) == 0 {
		return nil
	}
	book.Source = CostSourceCPAMPPriceBook
	raw, err := json.Marshal(book)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into settings(key, value, updated_at) values(?, ?, ?)
		on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`,
		cpampPriceBookCacheSetting, string(raw), time.Now().Unix())
	return err
}

func (s *Store) LoadCachedPriceBook(ctx context.Context) (PriceBook, bool, error) {
	if s == nil || s.db == nil {
		return PriceBook{}, false, nil
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `select value from settings where key=?`, cpampPriceBookCacheSetting).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return PriceBook{}, false, nil
		}
		return PriceBook{}, false, err
	}
	var book PriceBook
	if err := json.Unmarshal([]byte(raw), &book); err != nil {
		return PriceBook{}, false, err
	}
	if len(book.Prices) == 0 {
		return PriceBook{}, false, nil
	}
	book.Source = CostSourceCPAMPCachedPriceBook
	return book, true, nil
}

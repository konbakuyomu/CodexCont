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
	rows, err := db.QueryContext(ctx, `select
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		coalesce(source, ''), coalesce(source_model_id, ''), coalesce(updated_at_ms, 0), coalesce(synced_at_ms, 0)
		from model_prices order by model`)
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
	return book, nil
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

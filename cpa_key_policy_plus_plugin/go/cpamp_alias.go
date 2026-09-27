package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codexcont/cpa-key-policy-plus-plugin/internal/policyplus"
)

// As CPA's frontend auth provider, Key Policy+ hands CPA the key id as the
// request principal, so CPAMP's request monitor lists sha256(key id) where it
// used to list sha256(raw key). Pushing an alias for that hash lets the monitor
// name the caller again. The aliases CPAMP keeps for raw key hashes are never
// touched: native key names are read from them. CPAMP wants aliases unique,
// hence the suffix.

const (
	cpampAliasSuffix  = "·KP+"
	cpampAliasRefresh = 30 * time.Minute
)

var cpampHTTPClient = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}

var cpampAliasState = struct {
	sync.Mutex
	running bool
	pushed  map[string]string
	at      time.Time
	err     string
}{}

type cpampAliasItem struct {
	APIKeyHash string `json:"apiKeyHash"`
	Alias      string `json:"alias"`
}

func cpampPrincipalHash(keyID string) string {
	sum := sha256.Sum256([]byte(keyID))
	return hex.EncodeToString(sum[:])
}

// cpampAliasTargets maps each current key's principal hash to the alias the
// monitor should show.
func cpampAliasTargets(keys []policyplus.KeyRecord) map[string]string {
	out := map[string]string{}
	used := map[string]bool{}
	for _, key := range currentAdminKeyRows(keys, false) {
		name := strings.TrimSpace(key.Name)
		if name == "" {
			name = key.Preview
		}
		alias := name + cpampAliasSuffix
		if used[alias] {
			alias = fmt.Sprintf("%s %s%s", name, key.ID[max(0, len(key.ID)-6):], cpampAliasSuffix)
		}
		used[alias] = true
		out[cpampPrincipalHash(key.ID)] = alias
	}
	return out
}

func cpampAdminKey(cfg policyplus.Config) string {
	path := cfg.CPAMPAdminKeyFile
	if path == "" {
		path = filepath.Join(filepath.Dir(cfg.StateDBPath), "cpamp-admin-key")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// maybeSyncCPAMPAliases pushes aliases in the background when the key names
// changed since the last push or the last push is stale.
func maybeSyncCPAMPAliases(keys []policyplus.KeyRecord) {
	cfg := loadedConfig()
	token := cpampAdminKey(cfg)
	if token == "" || cfg.CPAMPURL == "" {
		return
	}
	want := cpampAliasTargets(keys)
	cpampAliasState.Lock()
	fresh := cpampAliasState.err == "" && time.Since(cpampAliasState.at) < cpampAliasRefresh && sameAliases(cpampAliasState.pushed, want)
	if cpampAliasState.running || fresh {
		cpampAliasState.Unlock()
		return
	}
	cpampAliasState.running = true
	cpampAliasState.Unlock()
	go func() {
		err := fmt.Errorf("同步中断")
		defer func() {
			_ = recover()
			finishCPAMPAliasSync(want, err)
		}()
		err = pushCPAMPAliases(context.Background(), cfg.CPAMPURL, token, want)
	}()
}

func finishCPAMPAliasSync(want map[string]string, err error) {
	cpampAliasState.Lock()
	defer cpampAliasState.Unlock()
	cpampAliasState.running = false
	cpampAliasState.at = time.Now()
	if err != nil {
		cpampAliasState.err = err.Error()
		return
	}
	cpampAliasState.err = ""
	cpampAliasState.pushed = want
}

func sameAliases(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for hash, alias := range b {
		if a[hash] != alias {
			return false
		}
	}
	return true
}

// pushCPAMPAliases merges want into CPAMP's alias list. CPAMP replaces the
// list on PUT, so every alias it already holds is sent back unchanged.
func pushCPAMPAliases(ctx context.Context, baseURL, token string, want map[string]string) error {
	endpoint := strings.TrimRight(baseURL, "/") + "/v0/management/api-key-aliases"
	var current struct {
		Items []cpampAliasItem `json:"items"`
	}
	if err := cpampCall(ctx, http.MethodGet, endpoint, token, nil, &current); err != nil {
		return err
	}
	items := make([]cpampAliasItem, 0, len(current.Items)+len(want))
	pending := map[string]string{}
	for hash, alias := range want {
		pending[hash] = alias
	}
	changed := false
	for _, item := range current.Items {
		if alias, ok := pending[item.APIKeyHash]; ok {
			delete(pending, item.APIKeyHash)
			if alias != item.Alias {
				item.Alias = alias
				changed = true
			}
		}
		items = append(items, cpampAliasItem{APIKeyHash: item.APIKeyHash, Alias: item.Alias})
	}
	for hash, alias := range pending {
		items = append(items, cpampAliasItem{APIKeyHash: hash, Alias: alias})
		changed = true
	}
	if !changed {
		return nil
	}
	return cpampCall(ctx, http.MethodPut, endpoint, token, map[string]any{"items": items}, nil)
}

func cpampCall(ctx context.Context, method, endpoint, token string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("CPAMP 地址无效")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cpampHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("连不上 CPAMP")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("CPAMP 管理员密钥无效")
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &failure)
		if failure.Code == "api_key_alias_duplicate" {
			return fmt.Errorf("CPAMP 里已有同名别名")
		}
		return fmt.Errorf("CPAMP 返回 HTTP %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("CPAMP 返回格式无法识别")
		}
	}
	return nil
}

func cpampAliasStatus() map[string]any {
	cfg := loadedConfig()
	out := map[string]any{"configured": cpampAdminKey(cfg) != "" && cfg.CPAMPURL != ""}
	cpampAliasState.Lock()
	defer cpampAliasState.Unlock()
	out["synced"] = len(cpampAliasState.pushed)
	if !cpampAliasState.at.IsZero() {
		out["at_ms"] = cpampAliasState.at.UnixMilli()
	}
	if cpampAliasState.err != "" {
		out["error"] = cpampAliasState.err
	}
	return out
}

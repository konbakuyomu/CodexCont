package policyplus

import (
	"strings"
	"time"
)

type Config struct {
	Enabled              bool   `yaml:"enabled"`
	ExclusiveAuth        bool   `yaml:"exclusive_auth"`
	StateDBPath          string `yaml:"state_db_path"`
	KeyPolicyStatePath   string `yaml:"key_policy_state_path"`
	LegacyQuotaDBPath    string `yaml:"legacy_quota_db_path"`
	GovernorStateDBPath  string `yaml:"governor_state_db_path"`
	CodexSummaryDBPath   string `yaml:"codex_summary_db_path"`
	NativeKeysConfigPath string `yaml:"native_keys_config_path"`
	CPAMPAliasDBPath     string `yaml:"cpamp_alias_db_path"`
	CPAMPAliasDBPaths    string `yaml:"cpamp_alias_db_paths"`
	CPAMPPriceDBPath     string `yaml:"cpamp_price_db_path"`
	CPAMPPriceDBPaths    string `yaml:"cpamp_price_db_paths"`
	SessionSecret        string `yaml:"session_secret"`
	CodexContEnabled     bool   `yaml:"codexcont_enabled"`
	CodexContRoute       bool   `yaml:"codexcont_route"`
	CodexContURL         string `yaml:"codexcont_url"`
	FailMode             string `yaml:"fail_mode"`
	PollIntervalMS       int    `yaml:"poll_interval_ms"`
	// Sub2PoolURL and Sub2PoolAPIKey optionally show a Sub2Pool capacity
	// estimate next to the pool's own; it never feeds the ledger.
	Sub2PoolURL    string `yaml:"sub2pool_url"`
	Sub2PoolAPIKey string `yaml:"sub2pool_api_key"`
	// CPAMPURL and CPAMPAdminKeyFile let the plugin name its keys in CPAMP's
	// request monitor. The key file defaults to cpamp-admin-key next to the
	// state DB; without it nothing is pushed.
	CPAMPURL          string `yaml:"cpamp_url"`
	CPAMPAdminKeyFile string `yaml:"cpamp_admin_key_file"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		ExclusiveAuth:    true,
		StateDBPath:      "cpa-policyplus.sqlite",
		SessionSecret:    "change-me",
		CodexContEnabled: false,
		CodexContURL:     "http://codexcont:8787",
		FailMode:         "fallback",
		PollIntervalMS:   1500,
		CPAMPURL:         "http://cpamp:18317",
	}
}

func (c Config) Normalize() Config {
	if strings.TrimSpace(c.StateDBPath) == "" {
		c.StateDBPath = DefaultConfig().StateDBPath
	}
	c.FailMode = strings.ToLower(strings.TrimSpace(c.FailMode))
	if c.FailMode == "" {
		c.FailMode = "fallback"
	}
	c.CodexContURL = strings.TrimRight(strings.TrimSpace(c.CodexContURL), "/")
	if c.CodexContURL == "" {
		c.CodexContURL = DefaultConfig().CodexContURL
	}
	c.CodexSummaryDBPath = strings.TrimSpace(c.CodexSummaryDBPath)
	c.NativeKeysConfigPath = strings.TrimSpace(c.NativeKeysConfigPath)
	c.CPAMPAliasDBPath = strings.TrimSpace(c.CPAMPAliasDBPath)
	c.CPAMPAliasDBPaths = strings.TrimSpace(c.CPAMPAliasDBPaths)
	c.CPAMPPriceDBPath = strings.TrimSpace(c.CPAMPPriceDBPath)
	c.CPAMPPriceDBPaths = strings.TrimSpace(c.CPAMPPriceDBPaths)
	if c.PollIntervalMS <= 0 {
		c.PollIntervalMS = 1500
	}
	c.Sub2PoolURL = strings.TrimRight(strings.TrimSpace(c.Sub2PoolURL), "/")
	c.Sub2PoolAPIKey = strings.TrimSpace(c.Sub2PoolAPIKey)
	c.CPAMPURL = strings.TrimRight(strings.TrimSpace(c.CPAMPURL), "/")
	c.CPAMPAdminKeyFile = strings.TrimSpace(c.CPAMPAdminKeyFile)
	return c
}

func (c Config) AliasDBPaths() []string {
	c = c.Normalize()
	seen := map[string]bool{}
	var out []string
	add := func(value string) {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
		}) {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	add(c.CPAMPAliasDBPath)
	add(c.CPAMPAliasDBPaths)
	return out
}

func (c Config) PriceDBPaths() []string {
	c = c.Normalize()
	seen := map[string]bool{}
	var out []string
	add := func(value string) {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t'
		}) {
			part = strings.TrimSpace(part)
			if part == "" || seen[part] {
				continue
			}
			seen[part] = true
			out = append(out, part)
		}
	}
	add(c.CPAMPPriceDBPath)
	add(c.CPAMPPriceDBPaths)
	if len(out) == 0 {
		return c.AliasDBPaths()
	}
	return out
}

func SessionTTL() time.Duration {
	return 24 * time.Hour
}

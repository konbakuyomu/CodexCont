package executor

import (
	"strings"
	"time"
)

const EncryptedInclude = "reasoning.encrypted_content"

type Config struct {
	Enabled               bool   `yaml:"enabled"`
	RouteEnabled          bool   `yaml:"route_enabled"`
	StateDBPath           string `yaml:"state_db_path"`
	FailMode              string `yaml:"fail_mode"`
	TruncationStep        int    `yaml:"truncation_step"`
	MaxContinue           int    `yaml:"max_continue"`
	MinN                  int    `yaml:"min_n"`
	MaxN                  int    `yaml:"max_n"`
	MarkerText            string `yaml:"marker_text"`
	ForceIncludeEncrypted bool   `yaml:"force_include_encrypted"`
	MaxTotalOutputTokens  int    `yaml:"max_total_output_tokens"`
	PollIntervalMS        int    `yaml:"poll_interval_ms"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:               true,
		RouteEnabled:          false,
		StateDBPath:           "cpa-codexcont-executor.sqlite",
		FailMode:              "fallback",
		TruncationStep:        518,
		MaxContinue:           8,
		MinN:                  1,
		MarkerText:            "Continue thinking...",
		ForceIncludeEncrypted: true,
		PollIntervalMS:        1500,
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
	if c.TruncationStep <= 0 {
		c.TruncationStep = DefaultConfig().TruncationStep
	}
	if c.MaxContinue <= 0 {
		c.MaxContinue = DefaultConfig().MaxContinue
	}
	if c.MinN <= 0 {
		c.MinN = DefaultConfig().MinN
	}
	if strings.TrimSpace(c.MarkerText) == "" {
		c.MarkerText = DefaultConfig().MarkerText
	}
	if c.PollIntervalMS <= 0 {
		c.PollIntervalMS = DefaultConfig().PollIntervalMS
	}
	return c
}

func SessionTTL() time.Duration {
	return 24 * time.Hour
}

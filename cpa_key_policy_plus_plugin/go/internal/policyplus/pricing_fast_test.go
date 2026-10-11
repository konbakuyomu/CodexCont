package policyplus

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"testing"
)

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-12 }

func fastTestPrice() ModelPrice {
	return ModelPrice{
		Model: "gpt-6-astra", InputPerMillion: 1, OutputPerMillion: 2,
		ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 100, InputPerMillion: 10, OutputPerMillion: 20, InputConfigured: true, OutputConfigured: true}},
		ServiceTiers: []ModelPriceServiceTier{{
			Mode: "fast", ServiceTier: "priority", InputPerMillion: 3, OutputPerMillion: 6, InputConfigured: true, OutputConfigured: true,
			ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 100, InputPerMillion: 8, OutputPerMillion: 30, InputConfigured: true, OutputConfigured: true}},
		}},
	}
}

func TestFastTierUsesItsOwnLongContextPrice(t *testing.T) {
	book := PriceBook{Source: CostSourceCPAMPPriceBook, Prices: map[string]ModelPrice{"gpt-6-astra": fastTestPrice()}}
	cases := []struct {
		name      string
		input     int64
		tier      string
		want      float64
		rule      string
		threshold int64
	}{
		{"fast short", 50, "priority", (50*3 + 10*6) / perMillion, "fast/priority", 0},
		{"fast long", 101, "priority", (101*8 + 10*30) / perMillion, "fast/priority", 100},
		{"fast by mode name", 101, "fast", (101*8 + 10*30) / perMillion, "fast/priority", 100},
		{"standard long", 101, "default", (101*10 + 10*20) / perMillion, "", 100},
		{"standard short", 50, "auto", (50*1 + 10*2) / perMillion, "", 0},
	}
	for _, c := range cases {
		got, ok := CostForUsageFromPriceBook(book, TokenUsage{InputTokens: c.input, OutputTokens: 10}, "gpt-6-astra", c.tier)
		if !ok || !closeTo(got.Costs["total"], c.want) || got.ServiceTierRule != c.rule || got.ContextThresholdTokens != c.threshold {
			t.Fatalf("%s: got=%v ok=%v rule=%q threshold=%d want=%v", c.name, got.Costs["total"], ok, got.ServiceTierRule, got.ContextThresholdTokens, c.want)
		}
	}
	if !PriceAllowsAdmission(fastTestPrice(), "priority") {
		t.Fatal("fast tier with complete long-context prices should be admitted")
	}
	broken := fastTestPrice()
	broken.ServiceTiers[0].ContextTiers[0] = ModelPriceContextTier{ThresholdTokens: 100, InputConfigured: true, OutputConfigured: true}
	if PriceAllowsAdmission(broken, "priority") {
		t.Fatal("a fast long-context tier that prices nothing must not be admitted")
	}
}

func TestPremiumTierWithoutRuleIsUnpricedAtAnyLength(t *testing.T) {
	price := ModelPrice{
		Model: "gpt-6-astra", InputPerMillion: 1, OutputPerMillion: 2,
		ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 100, InputPerMillion: 10, OutputPerMillion: 20, InputConfigured: true, OutputConfigured: true}},
	}
	book := PriceBook{Source: CostSourceCPAMPPriceBook, Prices: map[string]ModelPrice{"gpt-6-astra": price}}
	for _, input := range []int64{50, 101} {
		if got, ok := CostForUsageFromPriceBook(book, TokenUsage{InputTokens: input, OutputTokens: 10}, "gpt-6-astra", "priority"); ok {
			t.Fatalf("input %d: priority without a rule must stay unpriced, got %#v", input, got)
		}
	}
}

func TestEstimatedPriorityCost(t *testing.T) {
	price := ModelPrice{
		Model: "gpt-6-astra", InputPerMillion: 1, OutputPerMillion: 2, CachePerMillion: 0.1,
		ContextTiers: []ModelPriceContextTier{{ThresholdTokens: 100, InputPerMillion: 10, OutputPerMillion: 20, InputConfigured: true, OutputConfigured: true}},
	}
	book := PriceBook{Source: CostSourceCPAMPPriceBook, Prices: map[string]ModelPrice{
		"gpt-6-astra": price,
		"gpt-6-fast":  fastTestPrice(),
	}}
	short, ok := EstimatedPriorityCost(book, TokenUsage{InputTokens: 50, CachedTokens: 20, OutputTokens: 10}, "gpt-6-astra", "priority")
	if !ok || !closeTo(short.Costs["total"], 2*(30*1+20*0.1+10*2)/perMillion) || short.ServiceTierRule != "estimated" || short.ServiceTierMultiplier != 2 || short.ServiceTier != "priority" {
		t.Fatalf("short estimate = %#v ok=%v", short, ok)
	}
	long, ok := EstimatedPriorityCost(book, TokenUsage{InputTokens: 101, OutputTokens: 10}, "gpt-6-astra", "fast")
	if !ok || !closeTo(long.Costs["total"], 2*(101*10+10*20)/perMillion) || long.ContextThresholdTokens != 100 {
		t.Fatalf("long estimate = %#v ok=%v", long, ok)
	}
	if _, ok := EstimatedPriorityCost(book, TokenUsage{InputTokens: 50, OutputTokens: 10}, "gpt-6-astra", "default"); ok {
		t.Fatal("standard requests are never estimated")
	}
	if _, ok := EstimatedPriorityCost(book, TokenUsage{InputTokens: 50, OutputTokens: 10}, "gpt-6-astra", "flex"); ok {
		t.Fatal("only priority tiers are estimated")
	}
	if _, ok := EstimatedPriorityCost(book, TokenUsage{InputTokens: 50, OutputTokens: 10}, "gpt-6-fast", "priority"); ok {
		t.Fatal("a model with a real fast price is never estimated")
	}
	if _, ok := EstimatedPriorityCost(book, TokenUsage{InputTokens: 50, OutputTokens: 10}, "unknown-model", "priority"); ok {
		t.Fatal("a model without any price cannot be estimated")
	}
	if price.InputPerMillion != 1 || price.ContextTiers[0].InputPerMillion != 10 {
		t.Fatalf("estimate must not modify the price book: %#v", price)
	}
}

func TestParseCPAMPContextTiersJSON(t *testing.T) {
	got := parseCPAMPContextTiersJSON(`[{"thresholdTokens":272000,"prompt":8,"completion":30,"cache":0.8,"cacheRead":0.8,"cacheCreation":10,"promptConfigured":true,"completionConfigured":true},{"thresholdTokens":0,"prompt":1}]`)
	if len(got) != 1 {
		t.Fatalf("tiers = %#v", got)
	}
	tier := got[0]
	if tier.ThresholdTokens != 272000 || tier.InputPerMillion != 8 || tier.OutputPerMillion != 30 || tier.CachePerMillion != 0.8 ||
		tier.CacheReadPerMillion != 0.8 || tier.CacheCreationPerMillion != 10 || !tier.InputConfigured || !tier.OutputConfigured || !tier.CacheReadConfigured {
		t.Fatalf("tier = %#v", tier)
	}
	for _, raw := range []string{"", "null", "  ", "{not json"} {
		if got := parseCPAMPContextTiersJSON(raw); got != nil {
			t.Fatalf("%q -> %#v", raw, got)
		}
	}
}

func TestCPAMPPriceBookLoadsServiceTierContextTiers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`create table model_prices (model text primary key, prompt_per_1m real not null, completion_per_1m real not null,
			cache_per_1m real not null, source text, source_model_id text, updated_at_ms integer not null, synced_at_ms integer)`,
		`create table model_price_service_tiers (model text not null, mode text not null, service_tier text not null,
			prompt_per_1m real not null, completion_per_1m real not null, cache_per_1m real not null, context_tiers_json text)`,
		`insert into model_prices values('gpt-6-astra', 1, 2, 0.1, 'test', 'gpt-6-astra', 1000, 2000)`,
		`insert into model_price_service_tiers values('gpt-6-astra', 'fast', 'priority', 3, 6, 0.3,
			'[{"thresholdTokens":100,"prompt":8,"completion":30,"promptConfigured":true,"completionConfigured":true}]')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	book, err := LoadCPAMPPriceBookFromSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	price, ok := book.PriceFor("gpt-6-astra")
	if !ok || len(price.ServiceTiers) != 1 || len(price.ServiceTiers[0].ContextTiers) != 1 {
		t.Fatalf("service tier context prices missing: %#v ok=%v", price, ok)
	}
	long, ok := CostForUsageFromPriceBook(book, TokenUsage{InputTokens: 101, OutputTokens: 10}, "gpt-6-astra", "priority")
	if !ok || !closeTo(long.Costs["total"], (101*8+10*30)/perMillion) || long.ServiceTierRule != "fast/priority" {
		t.Fatalf("fast long-context cost = %#v ok=%v", long, ok)
	}
}

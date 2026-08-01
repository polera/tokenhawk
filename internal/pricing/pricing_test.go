package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

func TestPriceExactModelAndCachedInput(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	u := c.Price(core.Claude, at, core.Usage{Model: "claude-sonnet-4-20250514", Input: 1_000_000, CachedInput: 200_000, CacheCreation: 100_000, Output: 10_000})
	want := 800_000.0/1e6*3 + 200_000.0/1e6*.3 + 100_000.0/1e6*3.75 + 10_000.0/1e6*15
	if math.Abs(u.CostUSD-want) > 1e-9 || u.PricingStatus != "priced" {
		t.Fatalf("got %#v want cost %f", u, want)
	}
	unknown := c.Price(core.Codex, at, core.Usage{Model: "gpt-5-future", Total: 10})
	if unknown.PricingStatus != "unpriced" || unknown.CostUSD != 0 {
		t.Fatalf("unknown model was guessed: %#v", unknown)
	}
}

func TestLookupReturnsTheEffectiveRateUsedByPrice(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Lookup(core.Claude, time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC), "claude-sonnet-5"); ok {
		t.Fatal("Claude Sonnet 5 was priced before launch")
	}
	before, ok := c.Lookup(core.Claude, time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC), "claude-sonnet-5")
	if !ok || before.Input != 2 || before.Output != 10 || before.EffectiveFrom != "2026-06-30" {
		t.Fatalf("unexpected introductory rate: %#v, found=%v", before, ok)
	}
	after, ok := c.Lookup(core.Claude, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "claude-sonnet-5")
	if !ok || after.Input != 3 || after.Output != 15 || after.EffectiveFrom != "2026-09-01" {
		t.Fatalf("unexpected standard rate: %#v, found=%v", after, ok)
	}
	if _, ok := c.Lookup(core.Codex, time.Now(), "unknown-model"); ok {
		t.Fatal("unknown model unexpectedly matched a catalog rate")
	}
}

func TestAgyUsesUnderlyingModelProviderRate(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	gemini := c.Price(core.Agy, at, core.Usage{Model: "gemini-3.5-flash", Input: 1_000_000})
	if gemini.PricingStatus != "priced" || gemini.CostUSD != 1.5 {
		t.Fatalf("AGY Gemini usage was not priced with Gemini rates: %#v", gemini)
	}
	claude := c.Price(core.Agy, at, core.Usage{Model: "claude-sonnet-4-6", Input: 1_000_000})
	if claude.PricingStatus != "priced" || claude.CostUSD != 3 {
		t.Fatalf("AGY Claude usage was not priced with Claude rates: %#v", claude)
	}
}

func TestOpenCodeUsesUnderlyingModelProviderRate(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		model string
		want  float64
	}{
		{"openai/gpt-5.6-sol", 5},
		{"anthropic/claude-sonnet-4-6", 3},
		{"google/gemini-3.5-flash", 1.5},
	} {
		u := c.Price(core.OpenCode, at, core.Usage{Model: tc.model, Input: 1_000_000})
		if u.PricingStatus != "priced" || math.Abs(u.CostUSD-tc.want) > 1e-9 {
			t.Errorf("OpenCode %s priced as %#v, want $%.2f", tc.model, u, tc.want)
		}
	}
	unknown := c.Price(core.OpenCode, at, core.Usage{Model: "openrouter/some-model", Input: 1_000_000})
	if unknown.PricingStatus != "unpriced" || unknown.CostUSD != 0 {
		t.Fatalf("unknown OpenCode provider was guessed: %#v", unknown)
	}
}

func TestGeminiThoughtsAreBilledAsOutput(t *testing.T) {
	c, _ := Load("")
	u := c.Price(core.Gemini, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), core.Usage{Model: "gemini-2.5-pro", Output: 100, Reasoning: 50})
	if math.Abs(u.CostUSD-0.0015) > 1e-9 {
		t.Fatalf("got %f", u.CostUSD)
	}
}

func TestClaudeOpus5Rate(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Lookup(core.Claude, time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC), "claude-opus-5"); ok {
		t.Fatal("Claude Opus 5 was priced before its effective date")
	}
	rate, ok := c.Lookup(core.Claude, time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC), "claude-opus-5")
	if !ok || rate.EffectiveFrom != "2026-07-24" || rate.Input != 5 || rate.CachedInput != .5 || rate.CacheCreation != 6.25 || rate.Output != 25 {
		t.Fatalf("unexpected Claude Opus 5 rate: %#v, found=%v", rate, ok)
	}
}

func TestCatalogDoesNotPriceModelsBeforeAvailability(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider core.Provider
		model    string
		first    string
	}{
		{core.Claude, "claude-opus-5", "2026-07-24"},
		{core.Claude, "claude-opus-4-8", "2026-05-28"},
		{core.Claude, "claude-sonnet-5", "2026-06-30"},
		{core.Claude, "claude-haiku-4-5-20251001", "2025-10-15"},
		{core.Claude, "claude-opus-4-1-20250805", "2025-08-05"},
		{core.Claude, "claude-opus-4-20250514", "2025-05-22"},
		{core.Claude, "claude-sonnet-4-20250514", "2025-05-22"},
		{core.Claude, "claude-3-5-haiku-20241022", "2024-11-04"},
		{core.Codex, "gpt-5", "2025-08-07"},
		{core.Codex, "gpt-5.1-codex-max", "2025-11-19"},
		{core.Codex, "gpt-5.2", "2025-12-11"},
		{core.Codex, "gpt-5.3-codex", "2026-02-05"},
		{core.Codex, "gpt-5.6-sol", "2026-06-26"},
		{core.Codex, "o4-mini", "2025-04-16"},
		{core.Gemini, "gemini-3-flash-preview", "2025-12-17"},
		{core.Gemini, "gemini-3-pro-preview", "2025-11-18"},
		{core.Gemini, "gemini-2.5-pro", "2025-06-17"},
		{core.Gemini, "gemini-2.5-flash", "2025-06-17"},
		{core.Gemini, "gemini-3.5-flash", "2026-05-19"},
	} {
		first, err := time.Parse("2006-01-02", tc.first)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Lookup(tc.provider, first.AddDate(0, 0, -1), tc.model); ok {
			t.Errorf("%s/%s was priced before %s", tc.provider, tc.model, tc.first)
		}
		rate, ok := c.Lookup(tc.provider, first, tc.model)
		if !ok || rate.EffectiveFrom != tc.first {
			t.Errorf("%s/%s launch rate = %#v, found=%v; want effective %s", tc.provider, tc.model, rate, ok, tc.first)
		}
	}
}

func TestClaudeHaiku35PriceChange(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	initial, ok := c.Lookup(core.Claude, time.Date(2024, 12, 2, 0, 0, 0, 0, time.UTC), "claude-3-5-haiku-20241022")
	if !ok || initial.EffectiveFrom != "2024-11-04" || initial.Input != 1 || initial.CachedInput != .1 || initial.CacheCreation != 1.25 || initial.Output != 5 {
		t.Fatalf("unexpected initial Claude 3.5 Haiku rate: %#v, found=%v", initial, ok)
	}
	reduced, ok := c.Lookup(core.Claude, time.Date(2024, 12, 3, 0, 0, 0, 0, time.UTC), "claude-3-5-haiku-20241022")
	if !ok || reduced.EffectiveFrom != "2024-12-03" || reduced.Input != .8 || reduced.CachedInput != .08 || reduced.CacheCreation != 1 || reduced.Output != 4 {
		t.Fatalf("unexpected reduced Claude 3.5 Haiku rate: %#v, found=%v", reduced, ok)
	}
}

func TestCatalogPricesCurrentIndexedModelIDs(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		provider core.Provider
		model    string
		want     float64
	}{
		{core.Claude, "claude-opus-5", 5},
		{core.Claude, "claude-opus-4-8", 5},
		{core.Claude, "claude-sonnet-5", 2},
		{core.Claude, "claude-haiku-4-5-20251001", 1},
		{core.Codex, "gpt-5.1-codex-max", 1.25},
		{core.Codex, "gpt-5.2", 1.75},
		{core.Codex, "gpt-5.3-codex", 1.75},
		{core.Codex, "gpt-5.6-sol", 5},
		{core.Gemini, "gemini-3-flash-preview", .5},
		{core.Gemini, "gemini-3-pro-preview", 2},
	} {
		u := c.Price(tc.provider, at, core.Usage{Model: tc.model, Input: 1_000_000})
		if u.PricingStatus != "priced" || math.Abs(u.CostUSD-tc.want) > 1e-9 {
			t.Errorf("%s/%s priced as %#v, want $%.2f", tc.provider, tc.model, u, tc.want)
		}
	}
	sonnetStandard := c.Price(core.Claude, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), core.Usage{Model: "claude-sonnet-5", Input: 1_000_000})
	if math.Abs(sonnetStandard.CostUSD-3) > 1e-9 {
		t.Fatalf("Sonnet 5 post-introductory rate = %f, want 3", sonnetStandard.CostUSD)
	}
}

// Cache writes held for an hour bill at twice the base input rate rather than
// the 1.25x charged for the 5-minute default.
func TestLongCacheWritesUseTheHourlyRate(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	u := c.Price(core.Claude, at, core.Usage{Model: "claude-opus-4-8", CacheCreation: 1_000_000, CacheCreation1h: 600_000})
	want := 400_000.0/1e6*6.25 + 600_000.0/1e6*10
	if math.Abs(u.CostUSD-want) > 1e-9 {
		t.Fatalf("got %f want %f", u.CostUSD, want)
	}
	flat := c.Price(core.Claude, at, core.Usage{Model: "claude-opus-4-8", CacheCreation: 1_000_000})
	if math.Abs(flat.CostUSD-6.25) > 1e-9 {
		t.Fatalf("5-minute writes should stay at the base cache rate: %f", flat.CostUSD)
	}
}

// A rate that predates the split, such as a user override file, must fall back
// to its single cache rate instead of pricing hourly writes at zero.
func TestRateWithoutHourlyCacheFallsBackToTheShortRate(t *testing.T) {
	r := Rate{Provider: core.Claude, CacheCreation: 6.25}
	got := r.Estimate(core.Usage{CacheCreation: 1_000_000, CacheCreation1h: 1_000_000})
	if math.Abs(got-6.25) > 1e-9 {
		t.Fatalf("hourly writes were dropped: %f", got)
	}
}

// Every Claude model Claude Code can currently emit needs a rate, or its spend
// silently reports as zero.
func TestCatalogCoversCurrentClaudeModels(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	for _, model := range []string{
		"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-opus-4-5-20251101", "claude-sonnet-5", "claude-sonnet-4-6",
		"claude-sonnet-4-5-20250929", "claude-haiku-4-5-20251001",
	} {
		rate, ok := c.Lookup(core.Claude, at, model)
		if !ok {
			t.Errorf("%s has no catalog rate", model)
			continue
		}
		if rate.CacheCreation1h <= rate.CacheCreation {
			t.Errorf("%s hourly cache rate %v does not exceed the 5-minute rate %v", model, rate.CacheCreation1h, rate.CacheCreation)
		}
	}
}

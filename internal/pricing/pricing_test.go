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

func TestGeminiThoughtsAreBilledAsOutput(t *testing.T) {
	c, _ := Load("")
	u := c.Price(core.Gemini, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), core.Usage{Model: "gemini-2.5-pro", Output: 100, Reasoning: 50})
	if math.Abs(u.CostUSD-0.0015) > 1e-9 {
		t.Fatalf("got %f", u.CostUSD)
	}
}

func TestCatalogPricesCurrentIndexedModelIDs(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		provider core.Provider
		model    string
		want     float64
	}{
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

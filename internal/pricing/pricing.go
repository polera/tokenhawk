package pricing

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

//go:embed catalog.json
var bundled []byte

type Rate struct {
	Provider      core.Provider `json:"provider"`
	Model         string        `json:"model"`
	EffectiveFrom string        `json:"effective_from"`
	Input         float64       `json:"input_per_million"`
	CachedInput   float64       `json:"cached_input_per_million"`
	CacheCreation float64       `json:"cache_creation_per_million"`
	// CacheCreation1h prices cache writes held for an hour rather than the
	// 5-minute default. Rates that omit it fall back to CacheCreation so an
	// older override file under-reports nothing.
	CacheCreation1h float64 `json:"cache_creation_1h_per_million"`
	Output          float64 `json:"output_per_million"`
}

// LongCacheCreation is the 1-hour cache-write rate, defaulting to the
// 5-minute rate when a catalog entry does not distinguish the two.
func (r Rate) LongCacheCreation() float64 {
	if r.CacheCreation1h > 0 {
		return r.CacheCreation1h
	}
	return r.CacheCreation
}

type file struct {
	Version string `json:"version"`
	Rates   []Rate `json:"rates"`
}
type Catalog struct {
	version     string
	fingerprint string
	rates       []Rate
}

// Estimate applies a rate to usage in API billing units.
func (r Rate) Estimate(u core.Usage) float64 {
	standardInput := u.Input - u.CachedInput
	if standardInput < 0 {
		standardInput = 0
	}
	billedOutput := u.Output
	if r.Provider == core.Gemini {
		billedOutput += u.Reasoning
	}
	longCache := u.CacheCreation1h
	if longCache > u.CacheCreation {
		longCache = u.CacheCreation
	}
	shortCache := u.CacheCreation - longCache
	return (float64(standardInput)*r.Input +
		float64(u.CachedInput)*r.CachedInput +
		float64(shortCache)*r.CacheCreation +
		float64(longCache)*r.LongCacheCreation() +
		float64(billedOutput)*r.Output) / 1_000_000
}

func Load(override string) (*Catalog, error) {
	var base file
	if err := json.Unmarshal(bundled, &base); err != nil {
		return nil, err
	}
	if override != "" {
		// #nosec G304 -- override is a pricing file path the user explicitly points us at.
		b, err := os.ReadFile(override)
		if err != nil {
			return nil, err
		}
		var extra file
		if err := json.Unmarshal(b, &extra); err != nil {
			return nil, fmt.Errorf("pricing file: %w", err)
		}
		for _, add := range extra.Rates {
			replaced := false
			for i, old := range base.Rates {
				if old.Provider == add.Provider && old.Model == add.Model && old.EffectiveFrom == add.EffectiveFrom {
					base.Rates[i] = add
					replaced = true
				}
			}
			if !replaced {
				base.Rates = append(base.Rates, add)
			}
		}
		if extra.Version != "" {
			base.Version += "+" + extra.Version
		}
	}
	merged, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(merged)
	return &Catalog{version: base.Version, fingerprint: fmt.Sprintf("%x", sum), rates: base.Rates}, nil
}

func (c *Catalog) Version() string     { return c.version }
func (c *Catalog) Fingerprint() string { return c.fingerprint }

// Lookup returns the exact effective-dated catalog rate used to price a model.
// Keeping this selection in one place lets reports explain an estimate without
// duplicating (and potentially drifting from) the pricing rules.
func (c *Catalog) Lookup(provider core.Provider, at time.Time, model string) (Rate, bool) {
	var selected *Rate
	for i := range c.rates {
		r := &c.rates[i]
		if r.Provider != provider || !modelMatch(r.Model, model) {
			continue
		}
		eff, err := time.Parse("2006-01-02", r.EffectiveFrom)
		if err != nil || eff.After(at) {
			continue
		}
		if selected == nil || r.EffectiveFrom > selected.EffectiveFrom {
			selected = r
		}
	}
	if selected == nil {
		return Rate{}, false
	}
	return *selected, true
}

func (c *Catalog) Price(provider core.Provider, at time.Time, u core.Usage) core.Usage {
	selected, ok := c.Lookup(provider, at, u.Model)
	if !ok {
		u.PricingStatus = "unpriced"
		return u
	}
	u.CostUSD = selected.Estimate(u)
	u.PricingStatus = "priced"
	return u
}

func modelMatch(pattern, model string) bool {
	return pattern == model
}

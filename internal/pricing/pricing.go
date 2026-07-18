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
	Output        float64       `json:"output_per_million"`
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

func (c *Catalog) Price(provider core.Provider, at time.Time, u core.Usage) core.Usage {
	var selected *Rate
	for i := range c.rates {
		r := &c.rates[i]
		if r.Provider != provider || !modelMatch(r.Model, u.Model) {
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
		u.PricingStatus = "unpriced"
		return u
	}
	standardInput := u.Input - u.CachedInput
	if standardInput < 0 {
		standardInput = 0
	}
	billedOutput := u.Output
	if provider == core.Gemini {
		billedOutput += u.Reasoning
	}
	u.CostUSD = (float64(standardInput)*selected.Input + float64(u.CachedInput)*selected.CachedInput + float64(u.CacheCreation)*selected.CacheCreation + float64(billedOutput)*selected.Output) / 1_000_000
	u.PricingStatus = "priced"
	return u
}

func modelMatch(pattern, model string) bool {
	return pattern == model
}

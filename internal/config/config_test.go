package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnthropicAdminKeyComesFromEnvironmentAndLookbackFromConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_ADMIN_KEY", "admin-secret")
	cfg, err := Defaults()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicAdminKey != "admin-secret" || cfg.AnthropicCostLookbackDays != 31 {
		t.Fatalf("Anthropic defaults = key %q days %d", cfg.AnthropicAdminKey, cfg.AnthropicCostLookbackDays)
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err = os.WriteFile(path, []byte("anthropic_cost_lookback_days = 90\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = Load(path, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.AnthropicCostLookbackDays != 90 || cfg.AnthropicAdminKey != "admin-secret" {
		t.Fatalf("loaded Anthropic config = key %q days %d", cfg.AnthropicAdminKey, cfg.AnthropicCostLookbackDays)
	}
}

func TestAnthropicLookbackRejectsNonPositiveValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("anthropic_cost_lookback_days = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	if err := Load(path, &cfg); err == nil {
		t.Fatal("zero Anthropic lookback was accepted")
	}
}

package export

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polera/tokenhawk/internal/core"
)

func TestExportsContainUsageMetadataOnly(t *testing.T) {
	s := []core.Session{{
		Provider: core.Claude, ID: "id", Project: "/work", StartedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
		Usage:     []core.Usage{{Model: "m", Input: 1, Total: 2, PricingStatus: "unpriced"}},
		Subagents: []core.Subagent{{ID: "child", ParentID: "id", Name: "Explore", Running: true, Status: "running", Usage: []core.Usage{{Model: "child-model", Input: 3, Total: 4, CostUSD: .125, PricingStatus: "priced"}}}},
	}}
	for _, format := range []string{"json", "csv"} {
		p := filepath.Join(t.TempDir(), "out."+format)
		if err := Write(p, format, s); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		text := string(b)
		if !strings.Contains(text, "session_id") || strings.Contains(text, "prompt") || strings.Contains(text, "response") {
			t.Fatalf("unsafe or invalid %s export: %s", format, text)
		}
		for _, want := range []string{"child", "child-model", "0.125"} {
			if !strings.Contains(text, want) {
				t.Fatalf("%s export omitted subagent pricing field %q: %s", format, want, text)
			}
		}
	}
}

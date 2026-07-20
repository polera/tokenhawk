package upgrade

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTripAndSchedule(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "cache", "upgrade.json")
	state := State{CheckedAt: now, LatestVersion: "v1.2.3", DeferredUntil: now.Add(time.Hour)}
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LatestVersion != state.LatestVersion || !loaded.CheckedAt.Equal(now) {
		t.Fatalf("loaded state = %+v", loaded)
	}
	if loaded.ShouldCheck(now.Add(2 * time.Hour)) {
		t.Fatal("checked too soon")
	}
	if !loaded.ShouldCheck(now.Add(CheckInterval)) {
		t.Fatal("expected check after interval")
	}
}

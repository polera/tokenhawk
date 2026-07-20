package upgrade

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	CheckInterval = 24 * time.Hour
	DeferDuration = 24 * time.Hour
)

type State struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version,omitempty"`
	DeferredUntil time.Time `json:"deferred_until,omitempty"`
}

func StateFile() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokenhawk", "upgrade.json"), nil
}

func LoadState(path string) (State, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is the app's cache file.
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err = json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func SaveState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".upgrade-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (s State) ShouldCheck(now time.Time) bool {
	return !now.Before(s.DeferredUntil) && (s.CheckedAt.IsZero() || now.Sub(s.CheckedAt) >= CheckInterval)
}

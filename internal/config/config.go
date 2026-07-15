package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ClaudeDir     string
	CodexDir      string
	GeminiDir     string
	PiDir         string
	OpenCodeDB    string
	DBPath        string
	PricingFile   string
	ActiveWindow  time.Duration
	Refresh       time.Duration
	IncludeSource bool
}

func Defaults() (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return Config{}, err
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	piDir := os.Getenv("PI_CODING_AGENT_SESSION_DIR")
	if piDir == "" {
		piHome := os.Getenv("PI_CODING_AGENT_DIR")
		if piHome == "" {
			piHome = filepath.Join(home, ".pi", "agent")
		}
		piDir = filepath.Join(piHome, "sessions")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	return Config{
		ClaudeDir:    filepath.Join(home, ".claude", "projects"),
		CodexDir:     codex,
		GeminiDir:    filepath.Join(home, ".gemini", "tmp"),
		PiDir:        piDir,
		OpenCodeDB:   filepath.Join(dataHome, "opencode", "opencode.db"),
		DBPath:       filepath.Join(cache, "tokenhawk", "index.db"),
		ActiveWindow: 5 * time.Minute,
		Refresh:      2 * time.Second,
	}, nil
}

func DefaultFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tokenhawk", "config.toml"), nil
}

// Load applies the small TOML subset Tokenhawk emits: top-level key = value pairs.
func Load(path string, c *Config) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(strings.SplitN(s.Text(), "#", 2)[0])
		if line == "" || strings.HasPrefix(line, "[") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid config line %q", s.Text())
		}
		key, val := strings.TrimSpace(parts[0]), strings.Trim(strings.TrimSpace(parts[1]), "\"")
		switch key {
		case "claude_dir":
			c.ClaudeDir = expandHome(val)
		case "codex_dir":
			c.CodexDir = expandHome(val)
		case "gemini_dir":
			c.GeminiDir = expandHome(val)
		case "pi_dir":
			c.PiDir = expandHome(val)
		case "opencode_db":
			c.OpenCodeDB = expandHome(val)
		case "db_path":
			c.DBPath = expandHome(val)
		case "pricing_file":
			c.PricingFile = expandHome(val)
		case "active_window":
			d, e := time.ParseDuration(val)
			if e != nil {
				return e
			}
			c.ActiveWindow = d
		case "refresh":
			d, e := time.ParseDuration(val)
			if e != nil {
				return e
			}
			c.Refresh = d
		case "include_source":
			b, e := strconv.ParseBool(val)
			if e != nil {
				return e
			}
			c.IncludeSource = b
		}
	}
	return s.Err()
}

func expandHome(v string) string {
	if v == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(v, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(v, "~/"))
		}
	}
	return v
}

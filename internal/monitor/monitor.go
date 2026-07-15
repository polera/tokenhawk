package monitor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/polera/tokenhawk/internal/config"
	"github.com/polera/tokenhawk/internal/core"
	"github.com/polera/tokenhawk/internal/pricing"
	"github.com/polera/tokenhawk/internal/providers"
	"github.com/polera/tokenhawk/internal/store"
)

type Status struct {
	Scanning       bool
	Files, Updated int
	LastScan       time.Time
	Warning        string
}
type Monitor struct {
	cfg    config.Config
	store  *store.Store
	prices *pricing.Catalog
	mu     sync.RWMutex
	status Status
}

func New(cfg config.Config, s *store.Store, p *pricing.Catalog) *Monitor {
	return &Monitor{cfg: cfg, store: s, prices: p}
}
func (m *Monitor) Status() Status { m.mu.RLock(); defer m.mu.RUnlock(); return m.status }

func (m *Monitor) Scan(ctx context.Context) error {
	return m.scan(ctx, "")
}

// ScanProvider incrementally refreshes one provider and reconciles only that
// provider's derived rows. It is used by frequently invoked status commands.
func (m *Monitor) ScanProvider(ctx context.Context, provider core.Provider) error {
	return m.scan(ctx, provider)
}

func (m *Monitor) scan(ctx context.Context, only core.Provider) error {
	m.mu.Lock()
	m.status.Scanning = true
	m.status.Warning = ""
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.status.Scanning = false; m.status.LastScan = time.Now(); m.mu.Unlock() }()
	rebuilt, err := m.store.EnsurePricingFingerprint(m.prices.Fingerprint())
	if err != nil {
		return err
	}
	// A catalog change invalidates the whole derived index. Refill every
	// provider once instead of leaving unrelated providers empty after a
	// provider-specific status refresh.
	if rebuilt {
		only = ""
	}
	claudeDir, codexDir, geminiDir, piDir, openCodeDB := m.cfg.ClaudeDir, m.cfg.CodexDir, m.cfg.GeminiDir, m.cfg.PiDir, m.cfg.OpenCodeDB
	if only != "" {
		claudeDir, codexDir, geminiDir, piDir, openCodeDB = "", "", "", "", ""
		switch only {
		case core.Claude:
			claudeDir = m.cfg.ClaudeDir
		case core.Codex:
			codexDir = m.cfg.CodexDir
		case core.Gemini:
			geminiDir = m.cfg.GeminiDir
		case core.Pi:
			piDir = m.cfg.PiDir
		case core.OpenCode:
			openCodeDB = m.cfg.OpenCodeDB
		default:
			return fmt.Errorf("unsupported provider %q", only)
		}
	}
	discovered, err := providers.Discover(claudeDir, codexDir, geminiDir, piDir, openCodeDB)
	if err != nil {
		return err
	}
	var paths []string
	updated := 0
	for _, path := range discovered {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		provider := providers.ProviderFor(path, claudeDir, codexDir, geminiDir, piDir, openCodeDB)
		if provider == core.OpenCode {
			records, sources, parseErr := providers.ParseOpenCodeDB(path, func(source string, updated time.Time) bool {
				previous, sourceErr := m.store.Source(source)
				return sourceErr == nil && previous.SessionID != "" && previous.Offset == updated.UnixNano()
			})
			if parseErr != nil {
				return parseErr
			}
			paths = append(paths, sources...)
			for _, parsed := range records {
				priceParsed(m.prices, provider, &parsed)
				if err = m.store.Apply(ctx, parsed, st); err != nil {
					return err
				}
				updated++
			}
			continue
		}
		paths = append(paths, path)
		prev, err := m.store.Source(path)
		if err != nil {
			return err
		}
		if prev.Size == st.Size() && prev.ModTimeNS == st.ModTime().UnixNano() {
			continue
		}
		if st.Size() < prev.Offset {
			prev.Offset = 0
			prev.ParserState = ""
		}
		parsed, err := providers.Parse(path, provider, prev)
		if err != nil {
			m.warn(fmt.Sprintf("%s: %v", filepath.Base(path), err))
			continue
		}
		priceParsed(m.prices, provider, &parsed)
		if err = m.store.Apply(ctx, parsed, st); err != nil {
			return err
		}
		updated++
	}
	if only != "" {
		err = m.store.ReconcileProvider(ctx, paths, only)
	} else {
		err = m.store.Reconcile(ctx, paths)
	}
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.status.Files = len(discovered)
	m.status.Updated = updated
	m.mu.Unlock()
	return nil
}

func (m *Monitor) Run(ctx context.Context, onUpdate func()) error {
	if err := m.Scan(ctx); err != nil {
		return err
	}
	if onUpdate != nil {
		onUpdate()
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	m.addDirs(w)
	ticker := time.NewTicker(m.cfg.Refresh)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-w.Events:
			if !ok {
				return nil
			}
			dirty = true
			if e.Op&fsnotify.Create != 0 {
				if st, er := os.Stat(e.Name); er == nil && st.IsDir() {
					_ = addTree(w, e.Name)
				}
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			if err != nil {
				m.warn(err.Error())
			}
		case <-ticker.C:
			if dirty || time.Since(m.Status().LastScan) >= m.cfg.Refresh {
				if err = m.Scan(ctx); err != nil {
					m.warn(err.Error())
				}
				dirty = false
				if onUpdate != nil {
					onUpdate()
				}
			}
		}
	}
}
func (m *Monitor) addDirs(w *fsnotify.Watcher) {
	roots := []string{m.cfg.ClaudeDir, m.cfg.CodexDir, m.cfg.GeminiDir, m.cfg.PiDir}
	if m.cfg.OpenCodeDB != "" {
		roots = append(roots, filepath.Dir(m.cfg.OpenCodeDB))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		_ = addTree(w, root)
	}
}
func addTree(w *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return w.Add(path)
		}
		return nil
	})
}
func (m *Monitor) warn(v string) { m.mu.Lock(); m.status.Warning = v; m.mu.Unlock() }

func (m *Monitor) Sessions(ctx context.Context, f core.Filter) ([]core.Session, error) {
	return m.store.List(ctx, f, m.cfg.ActiveWindow, m.cfg.IncludeSource)
}

func priceParsed(catalog *pricing.Catalog, provider core.Provider, parsed *core.Parsed) {
	price := func(at time.Time, usage []core.Usage) {
		for i := range usage {
			if usage[i].PricingStatus != "reported" {
				usage[i] = catalog.Price(provider, at, usage[i])
			}
		}
	}
	if parsed.Subagent != nil {
		price(parsed.Subagent.UpdatedAt, parsed.Subagent.Usage)
		return
	}
	price(parsed.Session.UpdatedAt, parsed.Session.Usage)
}

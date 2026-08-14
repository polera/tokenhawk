package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mattn/go-isatty"
	"github.com/polera/tokenhawk/internal/config"
	"github.com/polera/tokenhawk/internal/core"
	exporter "github.com/polera/tokenhawk/internal/export"
	"github.com/polera/tokenhawk/internal/integration"
	"github.com/polera/tokenhawk/internal/monitor"
	"github.com/polera/tokenhawk/internal/pricing"
	"github.com/polera/tokenhawk/internal/sessionsearch"
	"github.com/polera/tokenhawk/internal/statusline"
	"github.com/polera/tokenhawk/internal/store"
	"github.com/polera/tokenhawk/internal/timerange"
	"github.com/polera/tokenhawk/internal/tui"
	"github.com/polera/tokenhawk/internal/upgrade"
)

// Main runs Tokenhawk and returns a process exit code. Both supported command
// package paths delegate here so their behavior cannot drift.
func Main(args []string, version string) int {
	if err := Run(args, version); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		fmt.Fprintln(os.Stderr, "tokenhawk:", err)
		return 1
	}
	return 0
}

func Run(args []string, version string) error {
	version = installedVersion(version)
	command := "tui"
	statuslineProvider := ""
	if len(args) > 0 {
		switch args[0] {
		case "export", "search", "status", "upgrade":
			command = args[0]
			args = args[1:]
		case "statusline":
			command = args[0]
			args = args[1:]
			if len(args) == 0 || strings.HasPrefix(args[0], "-") {
				return fmt.Errorf("statusline requires a provider: claude, codex, gemini, agy, pi, or opencode")
			}
			statuslineProvider = args[0]
			args = args[1:]
		case "wrap":
			rest := args[1:]
			noTmux := false
			if len(rest) > 0 && rest[0] == "--no-tmux" {
				noTmux = true
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return fmt.Errorf("wrap requires a provider: claude, codex, gemini, agy, pi, or opencode")
			}
			return integration.Wrap(rest[0], rest[1:], noTmux)
		case "version":
			fmt.Println(version)
			return nil
		}
	}
	if command == "upgrade" {
		upgradeFlags := flag.NewFlagSet("tokenhawk upgrade", flag.ContinueOnError)
		upgradeFlags.Usage = func() { fmt.Fprintln(upgradeFlags.Output(), "usage: tokenhawk upgrade") }
		if err := upgradeFlags.Parse(args); err != nil {
			return err
		}
		if upgradeFlags.NArg() != 0 {
			return fmt.Errorf("upgrade does not accept arguments")
		}
		return runUpgrade(version)
	}
	cfg, err := config.Defaults()
	if err != nil {
		return err
	}
	configPath, _ := config.DefaultFile()
	for i, a := range args {
		if a == "--config" && i+1 < len(args) {
			configPath = args[i+1]
		} else if strings.HasPrefix(a, "--config=") {
			configPath = strings.TrimPrefix(a, "--config=")
		}
	}
	if err = config.Load(configPath, &cfg); err != nil {
		return err
	}
	fs := flag.NewFlagSet("tokenhawk", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: tokenhawk [tui flags]")
		fmt.Fprintln(fs.Output(), "       tokenhawk export [flags]")
		fmt.Fprintln(fs.Output(), "       tokenhawk search [flags] <query>")
		fmt.Fprintln(fs.Output(), "       tokenhawk status [flags]")
		fmt.Fprintln(fs.Output(), "       tokenhawk statusline <claude|codex|gemini|agy|pi|opencode> [flags]")
		fmt.Fprintln(fs.Output(), "       tokenhawk wrap [--no-tmux] <claude|codex|gemini|agy|pi|opencode> [client arguments]")
		fmt.Fprintln(fs.Output(), "       tokenhawk upgrade")
		fmt.Fprintln(fs.Output(), "       tokenhawk version")
		fs.PrintDefaults()
	}
	fs.StringVar(&configPath, "config", configPath, "configuration file")
	fs.StringVar(&cfg.ClaudeDir, "claude-dir", cfg.ClaudeDir, "Claude projects directory")
	fs.StringVar(&cfg.CodexDir, "codex-dir", cfg.CodexDir, "Codex home directory")
	fs.StringVar(&cfg.GeminiDir, "gemini-dir", cfg.GeminiDir, "Gemini tmp directory")
	fs.StringVar(&cfg.AgyDir, "agy-dir", cfg.AgyDir, "Antigravity CLI data directory")
	fs.StringVar(&cfg.PiDir, "pi-dir", cfg.PiDir, "Pi sessions directory")
	fs.StringVar(&cfg.OpenCodeDB, "opencode-db", cfg.OpenCodeDB, "OpenCode SQLite database")
	fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "index database")
	fs.StringVar(&cfg.PricingFile, "pricing-file", cfg.PricingFile, "pricing override JSON")
	fs.IntVar(&cfg.AnthropicCostLookbackDays, "anthropic-cost-lookback-days", cfg.AnthropicCostLookbackDays, "UTC days to import from Anthropic on first sync")
	fs.DurationVar(&cfg.ActiveWindow, "active-window", cfg.ActiveWindow, "active session threshold")
	fs.DurationVar(&cfg.Refresh, "refresh", cfg.Refresh, "refresh interval")
	rebuild := fs.Bool("rebuild", false, "rebuild the index")
	provider := fs.String("provider", "", "filter provider")
	model := fs.String("model", "", "filter model")
	project := fs.String("project", "", "filter project")
	status := fs.String("status", "", "filter active or inactive")
	since := fs.String("since", "", "spend window, export, and search filter: RFC3339, YYYY-MM-DD, 7d, 3mo, today, mtd, ytd, or all")
	until := fs.String("until", "", "filter updates through RFC3339, YYYY-MM-DD, or a relative offset")
	formatDefault, formatHelp := "json", "export format: json or csv"
	if command == "status" {
		formatDefault, formatHelp = "plain", "status format: plain, ansi, tmux, or json"
	} else if command == "statusline" {
		formatDefault, formatHelp = "ansi", "status format: plain, ansi, tmux, or json"
	} else if command == "search" {
		formatDefault, formatHelp = "plain", "search format: plain or json"
	}
	format := fs.String("format", formatDefault, formatHelp)
	output := fs.String("output", "", "export output path")
	includeSource := fs.Bool("include-source", false, "include local source paths in JSON")
	sessionID := fs.String("session", "", "select an exact session ID")
	noScan := fs.Bool("no-scan", false, "render the existing index without scanning provider files")
	role := fs.String("role", "", "search only user or assistant messages")
	limit := fs.Int("limit", 100, "maximum search results")
	caseSensitive := fs.Bool("case-sensitive", false, "make transcript search case-sensitive")
	regex := fs.Bool("regex", false, "treat the transcript search query as a Go regular expression")
	if command == "statusline" {
		*provider = statuslineProvider
	}
	if err = fs.Parse(args); err != nil {
		return err
	}
	if cfg.AnthropicCostLookbackDays < 1 {
		return fmt.Errorf("--anthropic-cost-lookback-days must be positive")
	}
	if command == "tui" && interactiveTerminal() {
		if upgraded := offerUpgrade(version); upgraded {
			return nil
		}
	}
	cfg.IncludeSource = *includeSource
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if command == "search" {
		selectedProvider := core.Provider(*provider)
		if selectedProvider != "" && !supportedProvider(selectedProvider) {
			return fmt.Errorf("unsupported provider %q (expected claude, codex, gemini, agy, pi, or opencode)", selectedProvider)
		}
		if *status != "" {
			return fmt.Errorf("--status is not supported by search; use --since to bound transcript matches")
		}
		now := time.Now()
		sinceTime, parseErr := timerange.Parse(*since, now, false)
		if parseErr != nil {
			return fmt.Errorf("--since: %w", parseErr)
		}
		untilTime, parseErr := timerange.Parse(*until, now, true)
		if parseErr != nil {
			return fmt.Errorf("--until: %w", parseErr)
		}
		report, searchErr := sessionsearch.Search(ctx, cfg, sessionsearch.Query{
			Text: strings.Join(fs.Args(), " "), Provider: selectedProvider, SessionID: *sessionID,
			Project: *project, Role: *role, Since: sinceTime, Until: untilTime,
			Limit: *limit, CaseSensitive: *caseSensitive, Regex: *regex,
		})
		if searchErr != nil {
			return searchErr
		}
		for _, warning := range report.Warnings {
			fmt.Fprintln(os.Stderr, "tokenhawk: search:", warning)
		}
		return sessionsearch.Write(os.Stdout, *format, report)
	}
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer s.Close()
	if *rebuild {
		if err = s.Reset(); err != nil {
			return err
		}
	}
	prices, err := pricing.Load(cfg.PricingFile)
	if err != nil {
		return err
	}
	mon := monitor.New(cfg, s, prices)
	if command == "status" || command == "statusline" {
		selector := statusline.Selector{Provider: core.Provider(*provider), SessionID: *sessionID, Project: *project, Status: *status}
		if selector.Provider != "" && !supportedProvider(selector.Provider) {
			return fmt.Errorf("unsupported provider %q (expected claude, codex, gemini, agy, pi, or opencode)", selector.Provider)
		}
		if selector.Status != "" && selector.Status != "active" && selector.Status != "inactive" {
			return fmt.Errorf("unsupported status %q (expected active or inactive)", selector.Status)
		}
		if command == "statusline" && selector.Provider == core.Claude {
			fromClaude, parseErr := statusline.ParseClaude(os.Stdin)
			if parseErr != nil {
				return parseErr
			}
			if selector.SessionID == "" {
				selector.SessionID = fromClaude.SessionID
			}
			if selector.Project == "" {
				selector.Project = fromClaude.Project
			}
		}
		if command == "statusline" && selector.Provider == core.Agy {
			snapshot, parseErr := statusline.ParseAgy(os.Stdin)
			if parseErr != nil {
				return parseErr
			}
			if _, fingerprintErr := s.EnsurePricingFingerprint(prices.Fingerprint()); fingerprintErr != nil {
				return fingerprintErr
			}
			for i := range snapshot.Usage {
				snapshot.Usage[i] = prices.Price(core.Agy, snapshot.UpdatedAt, snapshot.Usage[i])
			}
			source := ""
			if filepath.Base(snapshot.ID) == snapshot.ID {
				source = filepath.Join(cfg.AgyDir, "conversations", snapshot.ID+".db")
			}
			if stat, statErr := os.Stat(source); statErr == nil && !stat.IsDir() {
				snapshot.SourcePath = source
				parsed := core.Parsed{
					Session:    snapshot,
					Provider:   core.Agy,
					SourcePath: source,
					Offset:     stat.Size(),
					Replace:    true,
				}
				if applyErr := s.Apply(ctx, parsed, stat); applyErr != nil {
					return applyErr
				}
			}
			line, renderErr := statusline.Render(snapshot, effectiveStatusFormat(*format))
			if renderErr != nil {
				return renderErr
			}
			fmt.Println(line)
			return nil
		}
		var scanErr error
		if !*noScan {
			if selector.Provider == "" {
				scanErr = mon.Scan(ctx)
			} else {
				scanErr = mon.ScanProvider(ctx, selector.Provider)
			}
		}
		sessions, listErr := mon.Sessions(ctx, core.Filter{Provider: selector.Provider})
		if listErr != nil {
			return listErr
		}
		selected, ok := statusline.Select(sessions, selector)
		if !ok {
			if scanErr != nil {
				return scanErr
			}
			line, renderErr := statusline.Waiting(selector, effectiveStatusFormat(*format))
			if renderErr != nil {
				return renderErr
			}
			fmt.Println(line)
			return nil
		}
		line, renderErr := statusline.Render(selected, effectiveStatusFormat(*format))
		if renderErr != nil {
			return renderErr
		}
		fmt.Println(line)
		return nil
	}
	now := time.Now()
	sinceTime, err := timerange.Parse(*since, now, false)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	// A bare --until date names a whole day, so it resolves to that day's end.
	untilTime, err := timerange.Parse(*until, now, true)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}
	if command == "export" {
		if *output == "" {
			return fmt.Errorf("--output is required for export")
		}
		if err = mon.Scan(ctx); err != nil {
			return err
		}
		sessions, err := mon.Sessions(ctx, core.Filter{Provider: core.Provider(*provider), Model: *model, Project: *project, Status: *status, Since: sinceTime, Until: untilTime})
		if err != nil {
			return err
		}
		if err = exporter.Write(*output, *format, sessions); err != nil {
			return err
		}
		fmt.Printf("exported %d sessions to %s\n", len(sessions), *output)
		return nil
	}
	m := tui.New(mon, prices)
	if *since != "" {
		// A --since on the interactive command is only meaningful as a spend
		// window, so open the view it describes.
		if m, err = m.WithSpendWindow(*since); err != nil {
			return fmt.Errorf("--since: %w", err)
		}
	}
	p := tea.NewProgram(m, tea.WithFilter(tui.FilterRefresh))
	go func() { _ = mon.Run(ctx, func() { p.Send(tui.RefreshMsg{}) }) }()
	_, err = p.Run()
	cancel()
	time.Sleep(10 * time.Millisecond)
	return err
}

func installedVersion(linked string) string {
	module := ""
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		module = info.Main.Version
	}
	return chooseInstalledVersion(linked, module)
}

func chooseInstalledVersion(linked, module string) string {
	if linked != "" && linked != "dev" {
		return linked
	}
	if module != "" && module != "(devel)" {
		return module
	}
	return linked
}

func runUpgrade(version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := upgrade.NewClient().Upgrade(ctx, version, "")
	if err != nil {
		return err
	}
	if !result.Updated {
		fmt.Printf("no upgrade available (installed %s; latest release %s)\n", result.Previous, result.Current)
		return nil
	}
	fmt.Printf("upgraded tokenhawk from %s to %s\n", result.Previous, result.Current)
	return nil
}

func offerUpgrade(version string) bool {
	if _, err := upgrade.Available(version, version); err != nil {
		return false
	}
	statePath, err := upgrade.StateFile()
	if err != nil {
		return false
	}
	state, err := upgrade.LoadState(statePath)
	if err != nil {
		state = upgrade.State{}
	}
	now := time.Now()
	if !state.ShouldCheck(now) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	client := upgrade.NewClient()
	release, err := client.Latest(ctx)
	cancel()
	if err != nil {
		return false
	}
	state.CheckedAt = now
	state.LatestVersion = release.Version
	_ = upgrade.SaveState(statePath, state)
	available, err := upgrade.Available(version, release.Version)
	if err != nil || !available {
		return false
	}

	fmt.Printf("A new Tokenhawk release is available: %s -> %s\n", version, release.Version)
	fmt.Print("Upgrade now? [y/N] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		state.DeferredUntil = now.Add(upgrade.DeferDuration)
		_ = upgrade.SaveState(statePath, state)
		fmt.Println("Upgrade deferred. Run `tokenhawk upgrade` at any time.")
		return false
	}

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	result, err := client.UpgradeTo(ctx, version, "", release)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tokenhawk: upgrade failed:", err)
		return false
	}
	if result.Updated {
		fmt.Printf("Upgraded tokenhawk from %s to %s. Restart tokenhawk to continue.\n", result.Previous, result.Current)
		return true
	}
	return false
}

func interactiveTerminal() bool {
	stdin := os.Stdin.Fd()
	stdout := os.Stdout.Fd()
	return (isatty.IsTerminal(stdin) || isatty.IsCygwinTerminal(stdin)) &&
		(isatty.IsTerminal(stdout) || isatty.IsCygwinTerminal(stdout))
}

func effectiveStatusFormat(format string) string {
	if format == "ansi" && os.Getenv("NO_COLOR") != "" {
		return "plain"
	}
	return format
}

func supportedProvider(provider core.Provider) bool {
	return provider == core.Claude || provider == core.Codex || provider == core.Gemini || provider == core.Agy || provider == core.Pi || provider == core.OpenCode
}

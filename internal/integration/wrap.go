package integration

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// Wrap runs a provider CLI with a live Tokenhawk tmux status bar. In an
// existing tmux session it temporarily replaces status-right; otherwise it
// creates and attaches to a dedicated session.
func Wrap(provider string, providerArgs []string) error {
	if provider != "claude" && provider != "codex" && provider != "gemini" && provider != "pi" && provider != "opencode" {
		return fmt.Errorf("unsupported provider %q (expected claude, codex, gemini, pi, or opencode)", provider)
	}
	client, err := exec.LookPath(provider)
	if err != nil {
		return fmt.Errorf("find %s CLI: %w", provider, err)
	}
	if _, err = exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tokenhawk wrap requires tmux: %w", err)
	}
	tokenhawk, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find tokenhawk executable: %w", err)
	}
	statusCommand := shellJoin([]string{tokenhawk, "status", "--provider", provider, "--project", "#{pane_current_path}", "--status", "active", "--format", "tmux"})
	if os.Getenv("TMUX") != "" {
		return wrapCurrentTmux(client, providerArgs, statusCommand)
	}
	return wrapNewTmux(client, provider, providerArgs, statusCommand)
}

func wrapCurrentTmux(client string, providerArgs []string, statusCommand string) error {
	oldStatus, err := tmuxValue("status")
	if err != nil {
		return err
	}
	oldInterval, err := tmuxValue("status-interval")
	if err != nil {
		return err
	}
	oldLength, err := tmuxValue("status-right-length")
	if err != nil {
		return err
	}
	oldRight, err := tmuxValue("status-right")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmuxSet("status", oldStatus)
		_ = tmuxSet("status-interval", oldInterval)
		_ = tmuxSet("status-right-length", oldLength)
		_ = tmuxSet("status-right", oldRight)
	}()
	if err = configureTmux("", statusCommand, false); err != nil {
		return err
	}
	cmd := exec.Command(client, providerArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	signals := make(chan os.Signal, 2)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		for {
			select {
			case value := <-signals:
				// The terminal normally delivers the signal to the child too;
				// forwarding also covers signals sent directly to the wrapper.
				_ = cmd.Process.Signal(value)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	return err
}

func wrapNewTmux(client, provider string, providerArgs []string, statusCommand string) error {
	name := fmt.Sprintf("tokenhawk-%s-%d", provider, os.Getpid())
	clientCommand := shellJoin(append([]string{client}, providerArgs...))
	if err := runTmux("new-session", "-d", "-s", name, clientCommand); err != nil {
		return err
	}
	configured := false
	defer func() {
		if !configured {
			_ = runTmux("kill-session", "-t", name)
		}
	}()
	if err := configureTmux(name, statusCommand, true); err != nil {
		return err
	}
	configured = true
	cmd := exec.Command("tmux", "attach-session", "-t", name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func configureTmux(target, statusCommand string, dedicated bool) error {
	args := func(values ...string) []string {
		if target == "" {
			return values
		}
		return append(values, "-t", target)
	}
	settings := [][2]string{
		{"status", "on"},
		{"status-interval", "2"},
		{"status-right-length", "160"},
		{"status-right", "#(" + statusCommand + ")"},
	}
	if dedicated {
		settings = append(settings,
			[2]string{"status-left", " #[fg=#05A9C7,bold]TOKENHAWK #[fg=#888888,nobold]"},
			[2]string{"status-style", "bg=#262b33,fg=#f8f8f2"},
		)
	}
	for _, setting := range settings {
		if err := runTmux(args("set-option", setting[0], setting[1])...); err != nil {
			return err
		}
	}
	return nil
}

func tmuxValue(option string) (string, error) {
	cmd := exec.Command("tmux", "show-option", "-gv", option)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read tmux %s: %w", option, err)
	}
	return strings.TrimSuffix(string(output), "\n"), nil
}

func tmuxSet(option, value string) error {
	return runTmux("set-option", option, value)
}

func runTmux(args ...string) error {
	cmd := exec.Command("tmux", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return strings.Join(quoted, " ")
}

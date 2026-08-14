//go:build !windows

package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

const statusRefresh = 2 * time.Second

// wrapPTY runs the client inside a pseudo-terminal one row shorter than the
// real terminal and keeps a live Tokenhawk status line on the reserved bottom
// row. It is the tmux-free fallback: the child believes the screen ends above
// the status row, and a DECSTBM scroll region keeps ordinary line output from
// pushing the status line away.
func wrapPTY(client, provider string, providerArgs []string) error {
	stdinFd := os.Stdin.Fd()
	if !term.IsTerminal(stdinFd) || !term.IsTerminal(os.Stdout.Fd()) {
		return errors.New("tokenhawk wrap without tmux requires an interactive terminal")
	}
	tokenhawk, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find tokenhawk executable: %w", err)
	}
	width, height, err := term.GetSize(stdinFd)
	if err != nil {
		return fmt.Errorf("read terminal size: %w", err)
	}
	if height < 4 {
		return errors.New("terminal is too short to reserve a status row")
	}
	project, err := os.Getwd()
	if err != nil {
		return err
	}
	// #nosec G204 -- client and providerArgs are the command line the user asked us to wrap.
	cmd := exec.Command(client, providerArgs...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(height - 1), Cols: uint16(width)}) // #nosec G115 -- terminal dimensions fit in uint16
	if err != nil {
		return fmt.Errorf("start %s in a pty: %w", client, err)
	}
	defer func() { _ = ptmx.Close() }()

	previous, err := term.MakeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("switch terminal to raw mode: %w", err)
	}
	screen := &statusScreen{out: os.Stdout, width: width, height: height}
	screen.reserve()
	defer func() {
		_ = term.Restore(stdinFd, previous)
		screen.release()
	}()

	// Keystrokes flow to the child unmodified; the goroutine unblocks when
	// the wrapper process exits.
	go func() { _, _ = io.Copy(ptmx, os.Stdin) }()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(statusRefresh)
		defer ticker.Stop()
		for {
			screen.paint(statusText(tokenhawk, provider, project))
			select {
			case <-done:
				return
			case <-winch:
				if newWidth, newHeight, sizeErr := term.GetSize(stdinFd); sizeErr == nil && newHeight >= 2 {
					_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(newHeight - 1), Cols: uint16(newWidth)}) // #nosec G115 -- terminal dimensions fit in uint16
					screen.resize(newWidth, newHeight)
				}
			case <-ticker.C:
			}
		}
	}()

	// Child output is forwarded through the screen so status painting never
	// interleaves inside an escape sequence. Reading EIO from the pty is the
	// normal end-of-session result once the child exits.
	_, _ = io.Copy(screen, ptmx)
	err = cmd.Wait()
	close(done)
	return err
}

// statusScreen serializes child output and status-line paints onto the real
// terminal. All escape output must hold the mutex.
type statusScreen struct {
	mu     sync.Mutex
	out    *os.File
	width  int
	height int
	last   string
}

func (s *statusScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.Write(p)
}

// reserve confines scrolling to the rows above the status line. DECSTBM homes
// the cursor, so the child's cursor position is saved and restored around it.
func (s *statusScreen) reserve() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "\x1b7\x1b[1;%dr\x1b8", s.height-1)
}

// release undoes the reservation and leaves the cursor on a cleared final row.
func (s *statusScreen) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.out, "\x1b7\x1b[r\x1b8\x1b[%d;1H\x1b[0m\x1b[2K", s.height)
}

func (s *statusScreen) resize(width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.width, s.height = width, height
	fmt.Fprintf(s.out, "\x1b7\x1b[1;%dr\x1b8", height-1)
	s.paintLocked()
}

func (s *statusScreen) paint(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = line
	s.paintLocked()
}

func (s *statusScreen) paintLocked() {
	fmt.Fprint(s.out, statusPaintSequence(s.last, s.width, s.height))
}

// statusPaintSequence writes one status row without disturbing the child:
// save cursor, draw the truncated line on the reserved bottom row with clean
// attributes, restore cursor.
func statusPaintSequence(line string, width, height int) string {
	content := ansi.Truncate(line, width, "…")
	return fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m\x1b8", height, content)
}

// statusText renders the current session line exactly the way the tmux
// integration does, by asking this executable for one status snapshot.
func statusText(tokenhawk, provider, project string) string {
	ctx, cancel := context.WithTimeout(context.Background(), statusRefresh-200*time.Millisecond)
	defer cancel()
	// #nosec G204 -- tokenhawk is this executable and the arguments are fixed flags.
	out, err := exec.CommandContext(ctx, tokenhawk, "status", "--provider", provider, "--project", project, "--status", "active", "--format", "ansi").Output()
	if err != nil {
		return "TOKENHAWK  status unavailable"
	}
	line, _, _ := strings.Cut(strings.TrimRight(string(out), "\r\n"), "\n")
	return line
}

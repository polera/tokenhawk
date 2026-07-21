package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellJoinPreservesProviderArguments(t *testing.T) {
	got := shellJoin([]string{"/usr/local/bin/codex", "--cd", "/work/it's here", "resume", "abc"})
	want := `'/usr/local/bin/codex' '--cd' '/work/it'"'"'s here' 'resume' 'abc'`
	if got != want {
		t.Fatalf("shellJoin() = %q, want %q", got, want)
	}
}

func TestDedicatedWrapperUsesStandardBlueBrandColor(t *testing.T) {
	dir := t.TempDir()
	tmux := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\nprintf '<%s>' \"$@\" >> \"$TMUX_CALLS\"\nprintf '\\n' >> \"$TMUX_CALLS\"\n"
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMUX_CALLS", calls)

	if err := configureTmux("tokenhawk-test", "tokenhawk status", true); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "<set-option><-t><tokenhawk-test><status-left>< #[fg=#05A9C7,bold]TOKENHAWK") {
		t.Fatalf("dedicated wrapper does not use standard blue:\n%s", output)
	}
}

func TestDedicatedWrapperPlacesTargetBeforeOption(t *testing.T) {
	dir := t.TempDir()
	tmux := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
if [ "$1" != "set-option" ] || [ "$2" != "-t" ] || [ "$3" != "tokenhawk-test" ] || [ "$4" = "" ] || [ "$5" = "" ] || [ "$6" != "" ]; then
	echo "invalid set-option arguments: $*" >&2
	exit 1
fi
`
	if err := os.WriteFile(tmux, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := configureTmux("tokenhawk-test", "tokenhawk status", true); err != nil {
		t.Fatal(err)
	}
}

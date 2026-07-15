package integration

import "testing"

func TestShellJoinPreservesProviderArguments(t *testing.T) {
	got := shellJoin([]string{"/usr/local/bin/codex", "--cd", "/work/it's here", "resume", "abc"})
	want := `'/usr/local/bin/codex' '--cd' '/work/it'"'"'s here' 'resume' 'abc'`
	if got != want {
		t.Fatalf("shellJoin() = %q, want %q", got, want)
	}
}

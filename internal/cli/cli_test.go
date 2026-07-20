package cli

import "testing"

func TestInstalledVersionPrefersLinkedRelease(t *testing.T) {
	tests := []struct {
		linked string
		module string
		want   string
	}{
		{linked: "v1.2.3", module: "v1.2.2", want: "v1.2.3"},
		{linked: "dev", module: "v1.2.2", want: "v1.2.2"},
		{linked: "dev", module: "(devel)", want: "dev"},
	}
	for _, test := range tests {
		if got := chooseInstalledVersion(test.linked, test.module); got != test.want {
			t.Fatalf("chooseInstalledVersion(%q, %q) = %q, want %q", test.linked, test.module, got, test.want)
		}
	}
}

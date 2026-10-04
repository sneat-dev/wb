package shared

import "testing"

func TestCwWtShortPathTrimsToOwnerSlashRepository(t *testing.T) {
	cases := map[string]string{
		"/tmp/projects/acme/app":        "acme/app",
		"/tmp/projects/acme/app/":       "acme/app",
		"/tmp/projects/acme/app/.//":    "acme/app",
		"app":                           "app",
		"":                              ".",
		"/":                             "",
		"relative/acme/app":             "acme/app",
		"/tmp/projects/acme/app/../app": "acme/app",
	}
	for input, want := range cases {
		if got := ShortPath(input); got != want {
			t.Errorf("ShortPath(%q) = %q, want %q", input, got, want)
		}
	}
}

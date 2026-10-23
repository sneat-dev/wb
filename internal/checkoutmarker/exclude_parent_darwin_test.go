//go:build darwin

package checkoutmarker

import (
	"errors"
	"os"
	"testing"
)

func TestNormalizeExcludeParentUsesOnlyFixedSystemAliases(t *testing.T) {
	t.Parallel()
	for _, alias := range []struct{ source, target string }{
		{"/var", "/private/var"},
		{"/tmp", "/private/tmp"},
	} {
		actual, err := os.Readlink(alias.source)
		if err != nil || (actual != "private"+alias.source && actual != alias.target) {
			t.Skipf("system alias %s is not installed: %q, %v", alias.source, actual, err)
		}
		if got := normalizeExcludeParent(alias.source); got != alias.target {
			t.Fatalf("normalize %s: got %s, want %s", alias.source, got, alias.target)
		}
		if got := normalizeExcludeParent(alias.source + "/fixture/info"); got != alias.target+"/fixture/info" {
			t.Fatalf("normalize child of %s: got %s", alias.source, got)
		}
		if got := normalizeExcludeParent(alias.source + "ious/fixture"); got != alias.source+"ious/fixture" {
			t.Fatalf("rewrote a prefix lookalike: %s", got)
		}
	}
	if got := normalizeExcludeParent("/Users/fixture/info"); got != "/Users/fixture/info" {
		t.Fatalf("rewrote unrelated parent: %s", got)
	}
}

func TestNormalizeExcludeParentRefusesUnexpectedSystemAlias(t *testing.T) {
	t.Parallel()
	for _, result := range []struct {
		name   string
		target string
		err    error
	}{
		{"unexpected target", "elsewhere", nil},
		{"readlink failure", "", errors.New("cannot inspect alias")},
	} {
		t.Run(result.name, func(t *testing.T) {
			t.Parallel()
			path := "/var/fixture/info"
			got := normalizeExcludeParentWithReadlink(path, func(name string) (string, error) {
				if name != "/var" {
					t.Fatalf("inspected unexpected alias %s", name)
				}
				return result.target, result.err
			})
			if got != path {
				t.Fatalf("rewrote untrusted alias to %s", got)
			}
		})
	}
}

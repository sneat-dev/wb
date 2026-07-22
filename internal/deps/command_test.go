package deps

import (
	"strings"
	"testing"
)

func TestGoCommandEnvironmentExtendsPrivateModuleSettings(t *testing.T) {
	t.Parallel()
	environment := goCommandEnvironment([]string{
		"PATH=/bin",
		"GOPRIVATE=example.org/internal",
		"GONOPROXY=example.net/private",
		"GONOSUMDB=example.com/legacy",
	}, []string{"github.com/sneat-co", "github.com/bots-go-framework,example.org/internal", "github.com/sneat-co"})

	values := environmentValues(environment)
	if got, want := values["GOPRIVATE"], "example.org/internal,github.com/sneat-co,github.com/bots-go-framework"; got != want {
		t.Fatalf("GOPRIVATE = %q, want %q", got, want)
	}
	if got, want := values["GONOPROXY"], "example.net/private,github.com/sneat-co,github.com/bots-go-framework,example.org/internal"; got != want {
		t.Fatalf("GONOPROXY = %q, want %q", got, want)
	}
	if got, want := values["GONOSUMDB"], "example.com/legacy,github.com/sneat-co,github.com/bots-go-framework,example.org/internal"; got != want {
		t.Fatalf("GONOSUMDB = %q, want %q", got, want)
	}
	if got, want := values["PATH"], "/bin"; got != want {
		t.Fatalf("PATH = %q, want %q", got, want)
	}
}

func TestGoCommandEnvironmentLeavesInheritedSettingsUntouchedWithoutPatterns(t *testing.T) {
	t.Parallel()
	base := []string{"GOPRIVATE=example.org/internal", "PATH=/bin"}
	got := goCommandEnvironment(base, nil)
	if strings.Join(got, "\n") != strings.Join(base, "\n") {
		t.Fatalf("environment = %q, want %q", got, base)
	}
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}

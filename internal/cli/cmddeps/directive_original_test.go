package cmddeps

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
)

func TestCwCovDirectiveMarker(t *testing.T) {
	t.Parallel()
	for verdict, want := range map[deps.DirectiveVerdict]string{deps.DirectiveCompliant: "-", deps.DirectiveWouldChange: "✓", deps.DirectiveCannotComply: "✗", deps.DirectiveBelowFloor: "▪", deps.DirectiveError: "x", "unknown": "x"} {
		if got := directiveMarker(verdict); got != want {
			t.Errorf("directiveMarker(%q) = %q, want %q", verdict, got, want)
		}
	}
}

func TestCwCovWriteDirectiveReportText(t *testing.T) {
	t.Parallel()
	rows := []depsrun.DirectiveRow{
		{Repository: "acme/app", Module: "github.com/acme/app", Verdict: string(deps.DirectiveCompliant), Detail: "compliant"},
		{Repository: "acme/app", Module: "github.com/acme/app/backend", Verdict: string(deps.DirectiveWouldChange), Detail: "would write go 1.26.0"},
		{Repository: "acme/blocked", Module: "github.com/acme/blocked", Verdict: string(deps.DirectiveCannotComply), Detail: "cannot comply", Forcing: []deps.ForcingDependency{{Path: "github.com/x/y", Version: "v1", GoVersion: "1.27"}}},
		{Repository: "acme/low", Module: "github.com/acme/low", Verdict: string(deps.DirectiveBelowFloor), Detail: "below the floor"},
		{Repository: "acme/broken", Module: "github.com/acme/broken", Verdict: string(deps.DirectiveError), Detail: "go list failed"},
		{Repository: "acme/nomodule", Verdict: "no-module", Detail: "no Go module"},
		{Repository: "acme/risky", Module: "github.com/acme/risky", Verdict: string(deps.DirectiveCompliant), Detail: "compliant", CodeQLAtRisk: true},
	}
	var out bytes.Buffer
	writeDirectiveReportText(&out, rows)
	text := out.String()
	for _, want := range []string{
		"acme/app (github.com/acme/app)", "acme/app (github.com/acme/app/backend)",
		"✓", "✗", "▪", "–",
		"7 module(s): ",
		"1 below-floor", "1 cannot-comply", "2 compliant", "1 error", "1 no-module", "1 would-change",
		"1 module(s) at risk under CodeQL default setup's pinned GOTOOLCHAIN=local",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("directive report missing %q:\n%s", want, text)
		}
	}
	// The footer's counts are sorted so a rerun reads the same.
	if strings.Index(text, "below-floor") > strings.Index(text, "cannot-comply") {
		t.Errorf("footer counts are not sorted:\n%s", text)
	}

	// No at-risk row means no at-risk line at all.
	out.Reset()
	writeDirectiveReportText(&out, []depsrun.DirectiveRow{{Repository: "acme/only", Verdict: string(deps.DirectiveCompliant), Detail: "compliant"}})
	if strings.Contains(out.String(), "at risk under CodeQL") {
		t.Errorf("at-risk line printed with no at-risk row:\n%s", out.String())
	}
}

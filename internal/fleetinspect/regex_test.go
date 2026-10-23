package fleetinspect

import (
	"strings"
	"testing"
)

func TestFleetRegex(t *testing.T) {
	t.Parallel()
	if expression, err := compileFleetRegex(""); err != nil || expression != nil {
		t.Fatalf("empty pattern = (%v, %v), want (nil, nil)", expression, err)
	}
	expression, err := compileFleetRegex(`^acme/`)
	if err != nil || expression == nil || !expression.MatchString("acme/app") {
		t.Fatalf("valid pattern = (%v, %v)", expression, err)
	}
	if _, err := compileFleetRegex(`(`); err == nil || !strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("invalid pattern error = %v, want a named --regex error", err)
	}
}

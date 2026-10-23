package checkoutsetup

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/worktrees"
	"testing"
)

func TestMarkCreatedCheckoutsEmptyPath(t *testing.T) {
	t.Parallel()
	errBuf := &bytes.Buffer{}
	results := []worktrees.CreateResult{
		{Repository: "demo", WorktreeDir: "", CanonicalDir: ""},
	}
	AfterCreate(checkoutmarker.DescribeOptions{BaseBranch: "main"}, errBuf, results, DefaultMarkerDependencies())
	if errBuf.Len() != 0 {
		t.Fatalf("expected no warning for an entirely empty checkout path, got %q", errBuf.String())
	}
}

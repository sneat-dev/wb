package worktreeretire

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktreebranches"
)

func TestTransactionReportReadPreservesNoFollowAndReadFailures(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	result := Transaction{Version: 1, Task: "task", Repository: "acme/app", Branch: "topic", SourceSHA: sha, Phase: "committed"}
	result.RetiredRef = worktreebranches.RetiredBranchDestination(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), result.Branch, sha)
	result.ArchiveRef = ArchiveRef(result)
	result.ReportPath = ReportPath(root, result)
	if err := WriteReport(result); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadReport(result.ReportPath); err != nil || got.SourceSHA != sha {
		t.Fatalf("read = (%+v, %v)", got, err)
	}
	boom := errors.New("reader failed")
	if _, err := readReportWithOps(result.ReportPath, reportReadOps{stat: func(*os.File) (os.FileInfo, error) { return nil, boom }, read: io.ReadAll}); !errors.Is(err, boom) {
		t.Fatalf("stat error = %v", err)
	}
	if _, err := readReportWithOps(result.ReportPath, reportReadOps{stat: (*os.File).Stat, read: func(io.Reader) ([]byte, error) { return nil, boom }}); !errors.Is(err, boom) {
		t.Fatalf("read error = %v", err)
	}
	alias := filepath.Join(root, "alias.json")
	if err := os.Symlink(result.ReportPath, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(alias); err == nil {
		t.Fatal("symlinked report was followed")
	}
}

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestBranchTransitionPublicationAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{{"", "(unknown)"}, {"abcdef0123456789", "abcdef012345"}} {
		if got := publicationSHA(tc.input); got != tc.want {
			t.Fatalf("publicationSHA(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestBranchTransitionEmptyHomeAdmission(t *testing.T) {
	t.Parallel()
	if _, _, _, _, err := activeWorkLogClaimAcrossHomes(wbhome.Resolution{}, "/unclaimed"); err == nil || !strings.Contains(err.Error(), "no resolved home") {
		t.Fatalf("active empty resolution = %v", err)
	}
	if _, _, _, err := claimForRelocationAcrossHomes(wbhome.Resolution{}, "/unclaimed"); err == nil || !strings.Contains(err.Error(), "no resolved home") {
		t.Fatalf("terminal empty resolution = %v", err)
	}
}

func TestBranchTransitionReportAndCopyNativeFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "directory-source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	control, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	_, controlErr := control.Read(make([]byte, 1))
	_ = control.Close()
	if controlErr == nil {
		t.Fatal("native directory read unexpectedly succeeded")
	}
	var controlPath *os.PathError
	if !errors.As(controlErr, &controlPath) {
		t.Fatalf("native cause lacks PathError: %v", controlErr)
	}
	if digest, err := fileSHA256(source); digest != "" || !errors.Is(err, controlPath.Err) {
		t.Fatalf("directory checksum = %q, %v; native cause %v", digest, err, controlPath.Err)
	}
	destination := filepath.Join(root, "copy")
	if digest, err := copyFileSHA256Injected(source, destination, nil); digest != "" || !errors.Is(err, controlPath.Err) {
		t.Fatalf("directory copy = %q, %v", digest, err)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() != 0 {
		t.Fatalf("failed native copy artifact = %v, %v", info, err)
	}
	invalidYear := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	report := filepath.Join(root, "invalid-time")
	if _, err := writeBranchCleanupReportInjected(report, BranchCleanupOptions{}, invalidYear, nil, nil); err == nil || !strings.Contains(err.Error(), "encode branch cleanup report") {
		t.Fatalf("invalid report timestamp = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(report, "cleanup.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid JSON published an artifact: %v", err)
	}
	// Remove only the still-unpublished pathname at the final file close. The
	// rename still runs natively and refuses its now missing source.
	report = filepath.Join(root, "publication")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Hook: func() {
		if err := os.Remove(filepath.Join(report, "cleanup.json.tmp")); err != nil {
			t.Fatal(err)
		}
	}}
	if _, err := writeBranchCleanupReportInjected(report, BranchCleanupOptions{}, time.Now(), nil, inj); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed native report source = %v", err)
	}
}

func TestBranchTransitionForkMetadataAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ raw, want string }{{`{"fork":false}`, ""}, {`{"fork":true}`, "refuses fork repository acme/app"}, {`{}`, "fork status is missing"}, {`{"fork":`, "decode repository fork status"}} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			err := admitReviewedRemoteForkMetadata([]byte(tc.raw), "acme/app")
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("response admission = %v, want %q", err, tc.want)
			}
		})
	}
}

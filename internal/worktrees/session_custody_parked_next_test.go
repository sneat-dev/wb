package worktrees

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestSessionCustodyNextAdmissionAndAggregateRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		source  session.Record
		persist func([]sessionpark.Worktree) error
		want    string
	}{
		{name: "missing persistence", want: "persistence callback"},
		{name: "invalid source", persist: func([]sessionpark.Worktree) error { return errors.New("persistence reached before source admission") }, want: "source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := CaptureParkedSessionAggregate(context.Background(), t.TempDir(), nil, tc.source, tc.persist); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("admission=%v want %q", err, tc.want)
			}
		})
	}
}

func TestSessionCustodyNextClaimAndPromptDescriptorFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	run, err := openDirectDirectoryNoFollow(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	if replay, err := publishPreparedTargetClaim(run, workLogClaim{ClaimID: strings.Repeat("a", 64)}, "conflict", ""); err == nil || replay {
		t.Fatalf("closed run admitted claim: replay=%t err=%v", replay, err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("refusal published children=%v err=%v", entries, err)
	}
	worktree := t.TempDir()
	directory, err := openJournalSubdirectory(worktree, promptsDirectory, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0).UTC()
	digest := sessionmove.DigestBytes([]byte("handover"))
	if err := validateExternalHandoverPrompt(worktree, at, "codex", "model", digest, []byte("handover")); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing prompt=%v", err)
	}
	hit := false
	var controlErr error
	err = validateExternalHandoverPromptWithDirectory(worktree, at, "codex", "model", digest, []byte("handover"), func(f *os.File) {
		hit = true
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		_, controlErr = f.Readdirnames(-1)
		if controlErr == nil {
			t.Fatal("native closed-directory control unexpectedly succeeded")
		}
	})
	nativeCause := controlErr
	if cause := errors.Unwrap(controlErr); cause != nil {
		nativeCause = cause
	}
	if !hit || nativeCause == nil || !errors.Is(err, nativeCause) {
		t.Fatalf("actual closed-directory observation hit=%t err=%v", hit, err)
	}

}

func TestSessionCustodyNextRemoteTipParserAdmission(t *testing.T) {
	t.Parallel()
	oid := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, raw, want string
		invalid         bool
	}{
		{name: "absent", raw: " \n"}, {name: "exact", raw: oid + "\trefs/heads/topic\n", want: oid},
		{name: "wrong ref", raw: oid + " refs/heads/other\n", invalid: true}, {name: "invalid object", raw: "bad refs/heads/topic\n", invalid: true},
		{name: "two tips", raw: oid + " refs/heads/topic\n" + oid + " refs/heads/topic\n", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseParkedRemoteBranchTip([]byte(tc.raw), "topic")
			if tc.invalid {
				if err == nil || err.Error() != "remote branch response was not one exact branch tip" {
					t.Fatalf("parser admission=%q,%v", got, err)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("parser=%q,%v want%q", got, err, tc.want)
			}
		})
	}
}

package worktreeretire

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func archiveFixture(t *testing.T) (string, Receipt, Ports) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	worktree := filepath.Join(root, "worktree")
	result := Receipt{Task: "task", Repository: "acme/app", Branch: "topic", SourceSHA: strings.Repeat("a", 40), RetiredRef: "retired/topic", Worktree: worktree, Canonical: filepath.Join(root, "canonical"), ArchiveRef: "retired/app/topic", ClaimID: "claim", EffortID: "effort", RunID: "run"}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(worktree, ".worktree.md"), "metadata")
	write(filepath.Join(worktree, ".wb", "local", "claim.json"), "local")
	write(filepath.Join(worktree, ".wb-worklog", "recovery.json"), "recovery")
	run := filepath.Join(home, "worklogs", "effort", "runs", "run")
	write(filepath.Join(run, "run.json"), "run")
	write(filepath.Join(run, "claims", "claim.json"), "claim")
	write(filepath.Join(run, "terminals", "claim.json"), "terminal")
	write(filepath.Join(run, "original-prompt.txt"), "prompt")
	write(filepath.Join(run, "reports", "report.md"), "report")
	write(filepath.Join(home, "worklogs", "effort", "outbox", "run-claim-finalized.json"), "outbox")
	sha := strings.Repeat("b", 40)
	ports := Ports{
		ReadClaim: func(_, _ string) (Claim, error) {
			return Claim{ClaimID: "claim", Repository: "acme/app", Branch: "topic", PromptArchive: "original-prompt.txt", PromptDigest: digest([]byte("prompt"))}, nil
		},
		ReadTerminal: func(_, _ string) (*Terminal, error) {
			return &Terminal{ClaimID: "claim", FinalCommit: result.SourceSHA, ReportPath: filepath.Join(run, "reports", "report.md")}, nil
		},
		ReportFileName: func(_, _ string) (string, error) { return "report.md", nil },
		RemoteSHA:      func(context.Context, string, string, string) (string, error) { return "", nil },
	}
	ports.Git = func(_ context.Context, working string, args ...string) (string, error) {
		switch args[0] {
		case "init", "config", "add", "commit", "push", "fetch":
			return "", nil
		case "rev-parse":
			return sha, nil
		case "cat-file":
			body, err := os.ReadFile(filepath.Join(working, "retirement.json"))
			return strconv.Itoa(len(body)), err
		case "ls-tree":
			var manifest Manifest
			body, err := os.ReadFile(filepath.Join(working, "retirement.json"))
			if err != nil {
				return "", err
			}
			if err := json.Unmarshal(body, &manifest); err != nil {
				return "", err
			}
			paths := []string{"retirement.json"}
			for path := range manifest.Files {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			return strings.Join(paths, "\x00") + "\x00", nil
		default:
			return "", fmt.Errorf("unexpected git command %q", args)
		}
	}
	ports.GitBytes = func(_ context.Context, working string, _ ...string) ([]byte, error) {
		return os.ReadFile(filepath.Join(working, "retirement.json"))
	}
	ports.GitObjectSHA = func(_ context.Context, working, object string) (string, error) {
		path := strings.TrimPrefix(object, "FETCH_HEAD:")
		body, err := os.ReadFile(filepath.Join(working, filepath.FromSlash(path)))
		return digest(body), err
	}
	return home, result, ports
}

func digest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestPublishArchiveProofAndFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, string, *Receipt, *Ports)
	}{
		{name: "new publication", want: "", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			calls := 0
			p.RemoteSHA = func(context.Context, string, string, string) (string, error) {
				calls++
				if calls == 1 {
					return "", nil
				}
				return strings.Repeat("b", 40), nil
			}
		}},
		{name: "existing publication", want: "", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.RemoteSHA = func(context.Context, string, string, string) (string, error) { return strings.Repeat("b", 40), nil }
		}},
		{name: "claim read", want: "claim unavailable", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReadClaim = func(string, string) (Claim, error) { return Claim{}, errors.New("claim unavailable") }
		}},
		{name: "claim changed", want: "claim identity changed", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReadClaim = func(string, string) (Claim, error) { return Claim{ClaimID: "other"}, nil }
		}},
		{name: "staging unavailable", want: "no such file", change: func(t *testing.T, home string, _ *Receipt, _ *Ports) {
			t.Setenv("TMPDIR", filepath.Join(home, "missing-staging"))
		}},
		{name: "git setup", want: "git setup unavailable", change: failGit("config", "git setup unavailable")},
		{name: "worktree metadata", want: "capture worktree metadata", change: func(t *testing.T, _ string, r *Receipt, _ *Ports) {
			if err := os.Remove(filepath.Join(r.Worktree, ".worktree.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "local log", want: "capture local Work Log", change: func(t *testing.T, _ string, r *Receipt, _ *Ports) {
			if err := os.RemoveAll(filepath.Join(r.Worktree, ".wb", "local")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "recovery", want: "capture Work Log projection", change: func(t *testing.T, _ string, r *Receipt, _ *Ports) {
			if err := os.Remove(filepath.Join(r.Worktree, ".wb-worklog", "recovery.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "report name", want: "invalid report name", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReportFileName = func(string, string) (string, error) { return "", errors.New("invalid report name") }
		}},
		{name: "run capture", want: "capture private Work Log run", change: func(t *testing.T, home string, _ *Receipt, _ *Ports) {
			if err := os.RemoveAll(filepath.Join(home, "worklogs", "effort", "runs", "run")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "mandatory terminal", want: "missing mandatory Work Log file", change: func(t *testing.T, home string, _ *Receipt, _ *Ports) {
			if err := os.Remove(filepath.Join(home, "worklogs", "effort", "runs", "run", "terminals", "claim.json")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "prompt digest", want: "prompt archive", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReadClaim = func(string, string) (Claim, error) {
				return Claim{ClaimID: "claim", Repository: "acme/app", Branch: "topic", PromptArchive: "original-prompt.txt", PromptDigest: "wrong"}, nil
			}
		}},
		{name: "terminal read", want: "terminal unavailable", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReadTerminal = func(string, string) (*Terminal, error) { return nil, errors.New("terminal unavailable") }
		}},
		{name: "terminal changed", want: "does not bind exact source", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReadTerminal = func(string, string) (*Terminal, error) { return &Terminal{ClaimID: "other"}, nil }
		}},
		{name: "report changed", want: "missing referenced private Work Log report", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.ReadTerminal = func(string, string) (*Terminal, error) {
				return &Terminal{ClaimID: "claim", FinalCommit: strings.Repeat("a", 40), ReportPath: "/other/report.md"}, nil
			}
		}},
		{name: "outbox capture", want: "capture Work Log outbox", change: func(t *testing.T, home string, _ *Receipt, _ *Ports) {
			if err := os.RemoveAll(filepath.Join(home, "worklogs", "effort", "outbox")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "manifest write", want: "manifest unavailable", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.WriteManifest = func(string, []byte, os.FileMode) error { return errors.New("manifest unavailable") }
		}},
		{name: "remote query", want: "remote unavailable", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.RemoteSHA = func(context.Context, string, string, string) (string, error) {
				return "", errors.New("remote unavailable")
			}
		}},
		{name: "existing proof", want: "fetch unavailable", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.RemoteSHA = func(context.Context, string, string, string) (string, error) { return strings.Repeat("b", 40), nil }
			old := p.Git
			p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "fetch" {
					return "", errors.New("fetch unavailable")
				}
				return old(ctx, dir, args...)
			}
		}},
		{name: "git add", want: "add unavailable", change: failGit("add", "add unavailable")},
		{name: "git commit", want: "commit unavailable", change: failGit("commit", "commit unavailable")},
		{name: "head query", want: "head unavailable", change: failGit("rev-parse", "head unavailable")},
		{name: "push", want: "publish private Work Log archive", change: failGit("push", "push unavailable")},
		{name: "postpush ref", want: "ref verification failed", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			p.RemoteSHA = func(context.Context, string, string, string) (string, error) { return "", nil }
		}},
		{name: "postpush proof", want: "fetch unavailable", change: func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
			calls := 0
			p.RemoteSHA = func(context.Context, string, string, string) (string, error) {
				calls++
				if calls == 1 {
					return "", nil
				}
				return strings.Repeat("b", 40), nil
			}
			old := p.Git
			p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "fetch" {
					return "", errors.New("fetch unavailable")
				}
				return old(ctx, dir, args...)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, result, ports := archiveFixture(t)
			tc.change(t, home, &result, &ports)
			err := PublishArchive(ctx, home, "private", &result, ports)
			if tc.want == "" {
				if err != nil || result.ArchiveSHA != strings.Repeat("b", 40) || result.Phase != "archive_published" {
					t.Fatalf("publish = (%+v, %v)", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || result.Phase == "archive_published" {
				t.Fatalf("publish = (%+v, %v), want %q without phase advance", result, err, tc.want)
			}
		})
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

type fakeGitObjectCommand struct {
	pipeErr    error
	startCalls int
	waitCalls  int
}

func (command *fakeGitObjectCommand) StdoutPipe() (io.ReadCloser, error) {
	return nil, command.pipeErr
}

func (command *fakeGitObjectCommand) Start() error {
	command.startCalls++
	return nil
}

func (command *fakeGitObjectCommand) Wait() error {
	command.waitCalls++
	return nil
}

func TestGitObjectPipeFailureStopsBeforeStartAndHash(t *testing.T) {
	pipeErr := errors.New("pipe descriptors exhausted")
	command := &fakeGitObjectCommand{pipeErr: pipeErr}
	got, err := gitObjectSHAWithCommand(command)
	if got != "" || !errors.Is(err, pipeErr) || command.startCalls != 0 || command.waitCalls != 0 {
		t.Fatalf("pipe failure = (digest %q, error %v, starts %d, waits %d)", got, err, command.startCalls, command.waitCalls)
	}
}

func TestCaptureAndGitObjectFailureBoundaries(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.WriteFile(source, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		ops  captureOps
		want string
	}{
		{name: "stat", ops: captureOps{stat: func(*os.File) (os.FileInfo, error) { return nil, errors.New("stat failed") }, copy: io.Copy}, want: "stat failed"},
		{name: "copy", ops: captureOps{stat: (*os.File).Stat, copy: func(io.Writer, io.Reader) (int64, error) { return 0, errors.New("copy failed") }}, want: "copy failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := captureFileWithOps(source, filepath.Join(root, tc.name+".copy"), nil, tc.ops); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("capture error = %v", err)
			}
		})
	}
	if _, err := hashGitObject(errorReader{}); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("hash read error = %v", err)
	}
	if _, err := hashGitObjectAndWait(errorReader{}, func() error { return nil }); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("object stream error = %v", err)
	}
	if _, err := hashGitObjectAndWait(strings.NewReader("private"), func() error { return errors.New("git failed") }); err == nil || !strings.Contains(err.Error(), "git failed") {
		t.Fatalf("object wait error = %v", err)
	}
	if got, err := hashGitObjectAndWait(strings.NewReader("private"), func() error { return nil }); err != nil || got != digest([]byte("private")) {
		t.Fatalf("object stream digest = (%q, %v)", got, err)
	}
	if got, err := hashGitObject(strings.NewReader("private")); err != nil || got != digest([]byte("private")) {
		t.Fatalf("hash = (%q, %v)", got, err)
	}
	if _, err := GitObjectSHA(context.Background(), root, "missing"); err == nil {
		t.Fatal("missing repository object accepted")
	}
	t.Setenv("PATH", "")
	if _, err := GitObjectSHA(context.Background(), root, "missing"); err == nil {
		t.Fatal("missing git executable accepted")
	}
}

func TestGitObjectSHAReadsExactBytes(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	source := filepath.Join(repo, "source")
	if err := os.WriteFile(source, []byte("private\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("git", "-C", repo, "hash-object", "-w", source).Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := GitObjectSHA(context.Background(), repo, strings.TrimSpace(string(output)))
	if err != nil || got != digest([]byte("private\n")) {
		t.Fatalf("object digest = (%q, %v)", got, err)
	}
}

func failGit(command, message string) func(*testing.T, string, *Receipt, *Ports) {
	return func(_ *testing.T, _ string, _ *Receipt, p *Ports) {
		old := p.Git
		p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
			if args[0] == command {
				return "", errors.New(message)
			}
			return old(ctx, dir, args...)
		}
	}
}

func TestVerifyArchiveFailureBoundaries(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, *Ports)
	}{
		{name: "fetch", want: "fetch failed", change: func(_ *testing.T, p *Ports) { failGit("fetch", "fetch failed")(nil, "", nil, p) }},
		{name: "commit changed", want: "commit changed", change: func(_ *testing.T, p *Ports) {
			old := p.Git
			p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "rev-parse" {
					return "wrong", nil
				}
				return old(ctx, dir, args...)
			}
		}},
		{name: "manifest size query", want: "size failed", change: func(_ *testing.T, p *Ports) { failGit("cat-file", "size failed")(nil, "", nil, p) }},
		{name: "manifest size invalid", want: "verification limit", change: func(_ *testing.T, p *Ports) {
			old := p.Git
			p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "cat-file" {
					return "invalid", nil
				}
				return old(ctx, dir, args...)
			}
		}},
		{name: "manifest read", want: "read failed", change: func(_ *testing.T, p *Ports) {
			p.GitBytes = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("read failed") }
		}},
		{name: "manifest json", want: "invalid character", change: func(_ *testing.T, p *Ports) {
			p.GitBytes = func(context.Context, string, ...string) ([]byte, error) { return []byte("not-json"), nil }
		}},
		{name: "manifest identity", want: "identity mismatch", change: func(_ *testing.T, p *Ports) {
			p.GitBytes = func(context.Context, string, ...string) ([]byte, error) { return []byte(`{"version":2}`), nil }
		}},
		{name: "tree query", want: "tree failed", change: func(_ *testing.T, p *Ports) { failGit("ls-tree", "tree failed")(nil, "", nil, p) }},
		{name: "tree mismatch", want: "unlisted file", change: func(_ *testing.T, p *Ports) {
			old := p.Git
			p.Git = func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "ls-tree" {
					return "retirement.json\x00extra\x00", nil
				}
				return old(ctx, dir, args...)
			}
		}},
		{name: "missing object", want: "file missing", change: func(_ *testing.T, p *Ports) {
			p.GitObjectSHA = func(context.Context, string, string) (string, error) { return "", errors.New("missing") }
		}},
		{name: "changed digest", want: "digest mismatch", change: func(_ *testing.T, p *Ports) {
			p.GitObjectSHA = func(context.Context, string, string) (string, error) { return "wrong", nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			working := t.TempDir()
			body := []byte(`{"version":1,"repository":"acme/app","branch":"topic","preserve":"branch","source_sha":"source","retired_ref":"retired/topic","claim_id":"claim","files":{"a":"hash"}}`)
			if err := os.WriteFile(filepath.Join(working, "retirement.json"), body, 0o600); err != nil {
				t.Fatal(err)
			}
			_, _, ports := archiveFixture(t)
			ports.GitObjectSHA = func(context.Context, string, string) (string, error) { return "hash", nil }
			manifest := Manifest{Version: 1, Repository: "acme/app", Branch: "topic", Preserve: "branch", SourceSHA: "source", RetiredRef: "retired/topic", ClaimID: "claim", Files: map[string]string{"a": "hash"}}
			tc.change(t, &ports)
			err := VerifyArchive(ctx, working, "private", "refs/heads/archive", strings.Repeat("b", 40), manifest, ports)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("verify error = %v, want %q", err, tc.want)
			}
		})
	}
}

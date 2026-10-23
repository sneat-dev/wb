package worktreerun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktreecollab"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
	"testing"
	"time"
)

func infoAuthority(t *testing.T) worktreecollab.Service {
	t.Helper()
	root := t.TempDir()
	return worktreecollab.Service{Store: worktreecollab.NewStore(filepath.Join(root, ".wb")), Ports: worktreecollab.ServicePorts{
		Resolve: func(context.Context, string) (worktreecollab.Checkout, error) {
			return worktreecollab.Checkout{ID: "checkout", Root: root, GitDir: root + "/gitdir", CommonDir: root + "/common"}, nil
		}, Caller: func() (string, error) { return "owner", nil }, Live: func(string) (bool, error) { return true, nil }, OwnerStatus: func(string) (string, error) { return "live", nil }, ObserveLegacy: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		}, ObserveLegacyForInspection: func(worktreecollab.Checkout) (worktreecollab.ObservedOwner, error) {
			return worktreecollab.ObservedOwner{}, nil
		}, Now: time.Now, NewMessageID: func() (string, error) { return "message", nil }}}
}
func TestInfoServicePreservesBoundaryOrderAndCanonicalException(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"load", "lane", "factory", "inspect", "canonical", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			sentinel := errors.New("unavailable " + stage)
			var calls []string
			authority := infoAuthority(t)
			if stage == "inspect" || stage == "canonical" {
				authority.Ports.Resolve = func(context.Context, string) (worktreecollab.Checkout, error) {
					if stage == "canonical" {
						return worktreecollab.Checkout{}, worktrees.ErrCollaborationCanonicalClone
					}
					return worktreecollab.Checkout{}, sentinel
				}
			}
			view := worktrees.WorkLogView{Worktree: "checkout"}
			lane := &orchestrate.MergeLaneClaim{Lane: "lane"}
			service := InfoService{ports: infoPorts{load: func(_ context.Context, q worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
				calls = append(calls, "load")
				if q.IncludePromptBodies || q.Worktree != "checkout" || q.ProjectsRoot != "projects" {
					t.Fatalf("redaction/root=%+v", q)
				}
				if stage == "load" {
					return worktrees.WorkLogView{}, sentinel
				}
				return view, nil
			}, lane: func(p string, v worktrees.WorkLogView) (*orchestrate.MergeLaneClaim, error) {
				calls = append(calls, "lane")
				if p != "projects" || v.Worktree != view.Worktree {
					t.Fatal("lane identity lost")
				}
				if stage == "lane" {
					return nil, sentinel
				}
				return lane, nil
			}, collaboration: func(p string) (worktreecollab.Service, error) {
				calls = append(calls, "factory")
				if p != "projects" {
					t.Fatal("root lost")
				}
				if stage == "factory" {
					return worktreecollab.Service{}, sentinel
				}
				return authority, nil
			}}}
			document, err := service.Inspect(context.Background(), InfoRequest{ProjectsRoot: "projects", Worktree: "checkout"})
			expectedCalls := 3
			if stage == "load" {
				expectedCalls = 1
			}
			if stage == "lane" {
				expectedCalls = 2
			}
			if len(calls) != expectedCalls {
				t.Fatalf("order=%v", calls)
			}
			if stage == "canonical" || stage == "success" {
				if err != nil || document.Worktree != "checkout" || document.MergerLaneClaim != lane {
					t.Fatalf("document=%+v err=%v", document, err)
				}
				if (document.Collaboration != nil) != (stage == "success") {
					t.Fatal("canonical omission changed")
				}
			} else if !errors.Is(err, sentinel) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
func TestInfoDefaultsSelectManifestIdentityAndLiveBranch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, view := range []worktrees.WorkLogView{{}, {Manifest: &worktrees.Manifest{Repository: "owner/repo"}}, {Manifest: &worktrees.Manifest{Repository: "owner/repo", Branch: "manifest"}}} {
		if claim, err := activeMergeLaneClaimForInfo(root, view); err != nil || claim != nil {
			t.Fatalf("claim=%+v err=%v", claim, err)
		}
	}
	service := DefaultInfoService()
	if _, err := service.Inspect(context.Background(), InfoRequest{ProjectsRoot: root, Worktree: filepath.Join(root, "missing")}); err == nil {
		t.Fatal("native missing checkout accepted")
	}
}

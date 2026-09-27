package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A single model covers the four states reconciliation may observe. Every
// injected failure must be reached, and no Git command outside the model is
// accepted; this exercises the refusal/receipt contract without mutating Git.
type defaultBranchReconcileModel struct {
	t           *testing.T
	mode        string
	failure     string
	failureAlso string
	override    string
	value       string
	calls       map[string]int
	checkpoints int
	failed      bool
	current     string
	localHead   string
	renamed     bool
	attached    bool
}

func (model *defaultBranchReconcileModel) failAt(key string) error {
	model.calls[key]++
	point := fmt.Sprintf("%s#%d", key, model.calls[key])
	if model.failure == point || model.failureAlso == point {
		model.failed = true
		return errors.New("injected " + key)
	}
	return nil
}

func (model *defaultBranchReconcileModel) git(_ context.Context, _ string, args ...string) (string, error) {
	call := strings.Join(args, " ")
	if err := model.failAt("git:" + call); err != nil {
		return "", err
	}
	if model.override == fmt.Sprintf("git:%s#%d", call, model.calls["git:"+call]) {
		return model.value, nil
	}
	switch call {
	case "fetch --prune origin", "remote set-head origin --auto", "status --porcelain", "branch --set-upstream-to=origin/main main":
		return "", nil
	case "worktree list --porcelain":
		return "worktree /canonical\nbranch refs/heads/master", nil
	case "branch --show-current":
		return model.current, nil
	case "rev-parse origin/main":
		return "same", nil
	case "rev-parse main":
		if model.mode == "attached" || model.mode == "detached-after" || model.renamed {
			return "same", nil
		}
		return "", errors.New("main does not exist")
	case "rev-parse master":
		return model.localHead, nil
	case "rev-parse HEAD":
		return "same", nil
	case "rev-parse --abbrev-ref --symbolic-full-name @{u}":
		return "origin/master", nil
	case "for-each-ref --format=%(refname:strip=2) refs/heads":
		return "master", nil
	case "merge --ff-only origin/main":
		model.localHead = "same"
		return "", nil
	case "log origin/main..master --not --remotes --format=%H":
		return "", nil
	default:
		model.t.Fatalf("unmodelled git call %q", call)
		return "", nil
	}
}

func (model *defaultBranchReconcileModel) install(t *testing.T) {
	oldGit, oldExists, oldAncestor, oldRename, oldAttach := defaultBranchGit, defaultBranchRefExists, defaultBranchIsAncestor, defaultBranchAtomicRenameRefs, defaultBranchAttachHead
	t.Cleanup(func() {
		defaultBranchGit, defaultBranchRefExists, defaultBranchIsAncestor, defaultBranchAtomicRenameRefs, defaultBranchAttachHead = oldGit, oldExists, oldAncestor, oldRename, oldAttach
	})
	defaultBranchGit = model.git
	defaultBranchRefExists = func(_ context.Context, _ string, ref string) (bool, error) {
		if err := model.failAt("ref:" + ref); err != nil {
			return false, err
		}
		switch ref {
		case "refs/heads/main":
			return model.mode == "detached-after" || model.renamed, nil
		case "refs/heads/master":
			return model.mode != "detached-after" && !model.renamed, nil
		default:
			t.Fatalf("unmodelled ref %q", ref)
			return false, nil
		}
	}
	defaultBranchIsAncestor = func(_ context.Context, _, _, _ string) (bool, error) {
		if err := model.failAt("ancestor"); err != nil {
			return false, err
		}
		return true, nil
	}
	defaultBranchAtomicRenameRefs = func(_ context.Context, _, _, _, _ string) error {
		if err := model.failAt("rename"); err != nil {
			return err
		}
		model.renamed = true
		model.current = ""
		return nil
	}
	defaultBranchAttachHead = func(_ context.Context, _, branch string) error {
		if err := model.failAt("attach"); err != nil {
			return err
		}
		model.current = branch
		model.attached = true
		return nil
	}
}

func TestReconcileDefaultBranchCanonicalFailureMatrix(t *testing.T) {
	for _, test := range []struct{ name, mode, failure, want string }{
		{"attached local ref read", "attached", "git:rev-parse main#1", "resolve local main"},
		{"attached repair plan", "attached", "checkpoint#1", "persist local tracking repair plan"},
		{"attached upstream repair", "attached", "git:branch --set-upstream-to=origin/main main#1", "restore local tracking branch"},
		{"attached repair receipt", "attached", "checkpoint#2", "injected checkpoint"},
		{"detached HEAD read", "detached-before", "git:rev-parse HEAD#1", "resolve detached HEAD"},
		{"detached destination ref inspection", "detached-before", "ref:refs/heads/main#1", "inspect detached local main"},
		{"detached source ref inspection", "detached-before", "ref:refs/heads/master#1", "inspect detached old local master"},
		{"detached source head read", "detached-before", "git:rev-parse master#1", "resolve detached local master"},
		{"detached restore plan", "detached-before", "checkpoint#1", "persist detached atomic rename recovery plan"},
		{"detached reattach source", "detached-before", "attach#1", "reattach detached HEAD"},
		{"detached restored attachment verification", "detached-before", "git:rev-parse HEAD#2", "verify restored local master attachment"},
		{"detached restore receipt", "detached-before", "checkpoint#2", "persist detached atomic rename recovery receipt"},
		{"detached renamed ref read", "detached-after", "git:rev-parse main#1", "resolve detached local main"},
		{"detached renamed restore plan", "detached-after", "checkpoint#1", "persist detached local reconciliation recovery plan"},
		{"detached renamed attach", "detached-after", "attach#1", "attach detached HEAD"},
		{"detached renamed attachment verification", "detached-after", "git:rev-parse HEAD#2", "verify recovered local main attachment"},
		{"detached renamed upstream", "detached-after", "git:branch --set-upstream-to=origin/main main#1", "set local tracking branch after detached recovery"},
		{"detached renamed receipt", "detached-after", "checkpoint#2", "persist detached local reconciliation receipt"},
		{"fast-forward ancestor check", "fast-forward", "ancestor#1", "classify local master"},
		{"fast-forward plan", "fast-forward", "checkpoint#1", "persist local fast-forward plan"},
		{"fast-forward merge", "fast-forward", "git:merge --ff-only origin/main#1", "fast-forward local master"},
		{"fast-forward local post-read", "fast-forward", "git:rev-parse master#2", "resolve local master after fast-forward"},
		{"fast-forward remote post-read", "fast-forward", "git:rev-parse origin/main#2", "resolve origin/main after fast-forward"},
		{"fast-forward receipt", "fast-forward", "checkpoint#2", "persist local fast-forward receipt"},
		{"rename plan", "rename", "checkpoint#1", "persist local-reconciliation plan"},
		{"rename local pre-read", "rename", "git:rev-parse master#2", "resolve local master before rename"},
		{"rename remote pre-read", "rename", "git:rev-parse origin/main#2", "resolve origin/main before rename"},
		{"atomic rename", "rename", "rename#1", "rename local default branch"},
		{"atomic rename receipt", "rename", "checkpoint#2", "persist atomic local rename receipt"},
		{"attach after rename", "rename", "attach#1", "attach HEAD to local main"},
		{"renamed attachment verification", "rename", "git:rev-parse HEAD#1", "verify renamed local main attachment"},
		{"tracking after rename", "rename", "git:branch --set-upstream-to=origin/main main#1", "set local tracking branch"},
		{"final reconciliation receipt", "rename", "checkpoint#3", "persist local-reconciliation receipt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &defaultBranchReconcileModel{t: t, mode: test.mode, failure: test.failure, calls: make(map[string]int), current: "master", localHead: "same"}
			if test.mode == "attached" {
				model.current = "main"
			}
			if strings.HasPrefix(test.mode, "detached") {
				model.current = ""
			}
			if test.mode == "fast-forward" {
				model.localHead = "old"
			}
			model.install(t)
			repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error {
				model.checkpoints++
				return model.failAt("checkpoint")
			})
			if !model.failed || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("failure %q: reached=%t err=%v, want %q (calls=%v)", test.failure, model.failed, err, test.want, model.calls)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalPersistsBlockedRecoveryFailures(t *testing.T) {
	for _, test := range []struct{ name, mode, first, second, want string }{
		{"old detached attachment", "detached-before", "git:rev-parse HEAD#2", "checkpoint#2", "persist blocked detached recovery receipt"},
		{"renamed detached attachment", "detached-after", "git:rev-parse HEAD#2", "checkpoint#2", "persist blocked detached recovery receipt"},
		{"post-rename attach", "rename", "attach#1", "checkpoint#3", "persist detached local rename receipt"},
		{"post-rename verification", "rename", "git:rev-parse HEAD#1", "checkpoint#3", "persist blocked local rename receipt"},
		{"post-rename tracking", "rename", "git:branch --set-upstream-to=origin/main main#1", "checkpoint#3", "persist partial local reconciliation receipt"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &defaultBranchReconcileModel{t: t, mode: test.mode, failure: test.first, failureAlso: test.second, calls: make(map[string]int), current: "master", localHead: "same"}
			if strings.HasPrefix(test.mode, "detached") {
				model.current = ""
			}
			model.install(t)
			repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error { return model.failAt("checkpoint") })
			if err == nil || !strings.Contains(err.Error(), test.want) || model.calls[strings.Split(test.second, "#")[0]] == 0 {
				t.Fatalf("dual failure %q + %q: err=%v entry=%+v calls=%v", test.first, test.second, err, entry, model.calls)
			}
			if entry.Disposition != "blocked" && entry.Disposition != "error" {
				t.Fatalf("failed recovery claimed compliance: %+v", entry)
			}
		})
	}
}

func TestReconcileDefaultBranchCanonicalRejectsMovedRefsAndAcceptsAlreadyTrackedHead(t *testing.T) {
	for _, test := range []struct{ name, mode, override, value, want string }{
		{"attached local moved", "attached", "git:rev-parse main#1", "moved", "run wb sync"},
		{"already tracked", "attached", "git:rev-parse --abbrev-ref --symbolic-full-name @{u}#1", "origin/main", ""},
		{"detached renamed head moved", "detached-after", "git:rev-parse main#1", "moved", "not the recorded atomic rename state"},
		{"fast-forward remote moved", "fast-forward", "git:rev-parse origin/main#2", "moved", "after fast-forward checkpoint"},
		{"pre-rename local moved", "rename", "git:rev-parse master#2", "moved", "before rename checkpoint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &defaultBranchReconcileModel{t: t, mode: test.mode, override: test.override, value: test.value, calls: make(map[string]int), current: "master", localHead: "same"}
			if test.mode == "attached" {
				model.current = "main"
			}
			if test.mode == "detached-after" {
				model.current = ""
			}
			if test.mode == "fast-forward" {
				model.localHead = "old"
			}
			model.install(t)
			repository := defaultBranchRepository{Repository: "acme/app", Desired: "main"}
			entry := defaultBranchCanonical{Path: "/canonical", Disposition: "blocked"}
			err := reconcileDefaultBranchCanonical(context.Background(), &repository, &entry, "master", "same", func() error { return nil })
			if model.calls[strings.Split(test.override, "#")[0]] == 0 {
				t.Fatalf("override %q was not reached: %v", test.override, model.calls)
			}
			if test.want == "" {
				if err != nil || entry.Disposition != "compliant" || !strings.Contains(strings.Join(entry.Actions, " "), "already reconciled") {
					t.Fatalf("already-tracked head = %+v err=%v", entry, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) || entry.Disposition == "compliant" {
				t.Fatalf("moved ref %q = %+v err=%v, want %q", test.override, entry, err, test.want)
			}
		})
	}
}

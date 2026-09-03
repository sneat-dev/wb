package worktreeend

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeInventory struct {
	worktrees []Worktree
	err       error
}

func (inventory fakeInventory) Worktrees(_ context.Context, _, _, repository string) ([]Worktree, error) {
	if inventory.err != nil {
		return nil, inventory.err
	}
	if repository == "" {
		return inventory.worktrees, nil
	}
	var narrowed []Worktree
	for _, worktree := range inventory.worktrees {
		if strings.EqualFold(worktree.Repository, repository) {
			narrowed = append(narrowed, worktree)
		}
	}
	return narrowed, nil
}

type fakeLinks struct {
	reasons map[string][]string
	err     error
}

func (links fakeLinks) LiveLinks(worktree string) ([]string, []string, error) {
	if links.err != nil {
		return nil, nil, links.err
	}
	found := links.reasons[worktree]
	if len(found) == 0 {
		return nil, nil, nil
	}
	return found, []string{"wb deps propagate local /lib --to " + worktree + " --undo"}, nil
}

// fakeCapture records the ORDER of capture and removal, which is the property
// that matters: a capture taken after removal is not a capture.
type fakeCapture struct {
	dirty       map[string][]string
	preserved   []string
	preserveErr map[string]error
	order       *[]string
}

func (capture fakeCapture) DirtyPaths(_ context.Context, worktree string) ([]string, error) {
	return capture.dirty[worktree], nil
}

func (capture *fakeCapture) Preserve(_ context.Context, worktree, _ string) (string, error) {
	if err := capture.preserveErr[worktree]; err != nil {
		return "", err
	}
	capture.preserved = append(capture.preserved, worktree)
	*capture.order = append(*capture.order, "capture "+worktree)
	return "stash-sha-" + worktree, nil
}

type fakeNotes struct{ sealed []string }

func (notes *fakeNotes) Seal(worktree, note string) (string, error) {
	notes.sealed = append(notes.sealed, worktree+": "+note)
	return worktree + "/.wb/prompts/0002-task-ended.md", nil
}

type fakeRetirer struct {
	retired []string
	err     map[string]error
	order   *[]string
}

func (retirer *fakeRetirer) Retire(_ context.Context, _, _, _, worktree string) error {
	if err := retirer.err[worktree]; err != nil {
		return err
	}
	retirer.retired = append(retirer.retired, worktree)
	*retirer.order = append(*retirer.order, "retire "+worktree)
	return nil
}

type fakeClaims struct{ released []string }

func (claims *fakeClaims) Release(_, task string) string {
	claims.released = append(claims.released, task)
	return "released"
}

func newEngine(t *testing.T, worktrees []Worktree) (*Engine, *fakeCapture, *fakeRetirer, *fakeClaims, *fakeNotes, *[]string) {
	t.Helper()
	order := &[]string{}
	capture := &fakeCapture{dirty: map[string][]string{}, preserveErr: map[string]error{}, order: order}
	retirer := &fakeRetirer{err: map[string]error{}, order: order}
	claims := &fakeClaims{}
	notes := &fakeNotes{}
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory:    fakeInventory{worktrees: worktrees},
		Links:        fakeLinks{reasons: map[string][]string{}},
		Capture:      capture,
		Notes:        notes,
		Retirer:      retirer,
		Claims:       claims,
	}
	return engine, capture, retirer, claims, notes, order
}

// The whole point of the verb: a dirty worktree is captured BEFORE it is
// retired, and the recoverable reference is reported.
func TestUncommittedWorkIsCapturedBeforeTheWorktreeIsRetired(t *testing.T) {
	engine, capture, retirer, claims, notes, order := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app", Branch: "feature/x"},
	})
	capture.dirty["/wt/app"] = []string{"main.go", "notes.md"}

	result, err := engine.End(context.Background(), Options{Task: "improve-login", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(*order) != 2 || (*order)[0] != "capture /wt/app" || (*order)[1] != "retire /wt/app" {
		t.Fatalf("order = %v, want the capture strictly before the removal", *order)
	}
	member := result.Members[0]
	if member.CaptureRef == "" {
		t.Fatal("no recoverable reference was reported")
	}
	if !member.Removed || member.Action != "ended" {
		t.Fatalf("member = %#v", member)
	}
	if len(retirer.retired) != 1 || len(claims.released) != 1 {
		t.Fatalf("retired=%v released=%v", retirer.retired, claims.released)
	}
	if len(notes.sealed) != 1 || !strings.Contains(notes.sealed[0], member.CaptureRef) {
		t.Errorf("the sealed note does not name the capture: %v", notes.sealed)
	}
}

// A capture that fails stops the removal. Retiring a checkout whose work could
// not be preserved is the one outcome that loses data irrecoverably.
func TestAFailedCaptureStopsTheRemoval(t *testing.T) {
	engine, capture, retirer, claims, _, _ := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app"},
	})
	capture.dirty["/wt/app"] = []string{"main.go"}
	capture.preserveErr["/wt/app"] = errors.New("disk full")

	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if !result.Failed() {
		t.Fatal("a failed capture was reported as success")
	}
	if len(retirer.retired) != 0 {
		t.Fatalf("the worktree was removed after the capture failed: %v", retirer.retired)
	}
	if len(claims.released) != 0 {
		t.Errorf("the claim was released while the worktree survives: %v", claims.released)
	}
}

// A live local link is the one refusal, and it fires before any side effect
// over ANY of the task's worktrees.
func TestALiveLinkRefusesTheWholeTaskBeforeAnySideEffect(t *testing.T) {
	engine, capture, retirer, claims, _, _ := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app"},
		{Repository: "acme/site", Path: "/wt/site"},
	})
	engine.Links = fakeLinks{reasons: map[string][]string{
		"/wt/site": {"stream s: acme/site links github.com/acme/library/backend (go.work)"},
	}}
	capture.dirty["/wt/app"] = []string{"main.go"}

	_, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	refusal := &Refusal{}
	if !errors.As(err, &refusal) || refusal.Code != RefusalLiveLink {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalLiveLink)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "--undo") {
		t.Errorf("refusal does not name the clearing command: %v", refusal.Sanctioned)
	}
	if len(capture.preserved) != 0 || len(retirer.retired) != 0 || len(claims.released) != 0 {
		t.Fatalf("a refused end still acted: captured=%v retired=%v released=%v",
			capture.preserved, retirer.retired, claims.released)
	}
}

// Without --apply nothing is changed and the report says what would happen.
func TestWithoutApplyNothingIsChanged(t *testing.T) {
	engine, capture, retirer, claims, notes, _ := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app"},
	})
	capture.dirty["/wt/app"] = []string{"main.go"}

	result, err := engine.End(context.Background(), Options{Task: "t"})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if result.Applied || result.Members[0].Action != "would-end" {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(result.Members[0].Detail, "would be captured") {
		t.Errorf("the dry run does not say the work would be captured: %q", result.Members[0].Detail)
	}
	if len(capture.preserved) != 0 || len(retirer.retired) != 0 || len(claims.released) != 0 || len(notes.sealed) != 0 {
		t.Fatal("a dry run mutated something")
	}
}

// The claim outlives a worktree that could not be retired: releasing it would
// advertise the task as free while its checkout is still on disk.
func TestTheClaimIsKeptWhenAWorktreeSurvives(t *testing.T) {
	engine, _, retirer, claims, _, _ := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app"},
		{Repository: "acme/site", Path: "/wt/site"},
	})
	retirer.err["/wt/site"] = errors.New("branch is not merged")

	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(claims.released) != 0 {
		t.Fatalf("the claim was released while a worktree survives: %v", claims.released)
	}
	if !strings.Contains(result.ClaimOutcome, "kept") {
		t.Errorf("claim outcome = %q, want it to say the claim was kept", result.ClaimOutcome)
	}
	if !result.Failed() {
		t.Error("a worktree that could not be retired was reported as success")
	}
}

// A clean worktree is retired with no capture at all.
func TestACleanWorktreeIsRetiredWithoutACapture(t *testing.T) {
	engine, capture, retirer, _, _, _ := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app"},
	})
	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(capture.preserved) != 0 {
		t.Errorf("a clean worktree was captured anyway: %v", capture.preserved)
	}
	if result.Members[0].CaptureRef != "" || !result.Members[0].Removed {
		t.Fatalf("member = %#v", result.Members[0])
	}
	if len(retirer.retired) != 1 {
		t.Fatalf("retired = %v", retirer.retired)
	}
}

// A note that cannot be sealed must not strand the worktree: the capture
// already exists, and leaving residue is what this verb removes.
func TestANoteThatCannotBeSealedStillRetiresTheWorktree(t *testing.T) {
	engine, _, retirer, _, _, _ := newEngine(t, []Worktree{{Repository: "acme/app", Path: "/wt/app"}})
	engine.Notes = failingNotes{}
	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(retirer.retired) != 1 {
		t.Fatalf("the worktree was stranded by a journal write: %v", retirer.retired)
	}
	if !strings.Contains(result.Members[0].Detail, "note not sealed") {
		t.Errorf("the failure was not reported: %q", result.Members[0].Detail)
	}
}

type failingNotes struct{}

func (failingNotes) Seal(string, string) (string, error) { return "", errors.New("read-only journal") }

func TestAnUnknownTaskIsAnError(t *testing.T) {
	engine, _, _, _, _, _ := newEngine(t, nil)
	if _, err := engine.End(context.Background(), Options{Task: "absent"}); err == nil {
		t.Fatal("ending a task with no worktrees reported success")
	}
}

func TestRepositoryNarrowsACoordinatedTask(t *testing.T) {
	engine, _, retirer, _, _, _ := newEngine(t, []Worktree{
		{Repository: "acme/app", Path: "/wt/app"},
		{Repository: "acme/site", Path: "/wt/site"},
	})
	if _, err := engine.End(context.Background(), Options{Task: "t", Repository: "acme/app", Apply: true}); err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(retirer.retired) != 1 || retirer.retired[0] != "/wt/app" {
		t.Fatalf("retired = %v, want only the named repository", retirer.retired)
	}
}

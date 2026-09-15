package worktreeend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// tailCovInventory is a per-test inventory that can report a failure and can
// count how many times it was consulted.
type tailCovInventory struct {
	worktrees []Worktree
	err       error
	calls     int
}

func (inventory *tailCovInventory) Worktrees(_ context.Context, _, _, _ string) ([]Worktree, error) {
	inventory.calls++
	if inventory.err != nil {
		return nil, inventory.err
	}
	return inventory.worktrees, nil
}

// tailCovLinks answers the live-link fence with per-worktree reasons and
// commands, or one shared failure.
type tailCovLinks struct {
	reasons    map[string][]string
	sanctioned map[string][]string
	err        error
}

func (links tailCovLinks) LiveLinks(worktree string) ([]string, []string, error) {
	if links.err != nil {
		return nil, nil, links.err
	}
	return links.reasons[worktree], links.sanctioned[worktree], nil
}

// tailCovCapture records the exact preservation message and can fail either
// the dirty-path probe or the capture itself.
type tailCovCapture struct {
	dirty       map[string][]string
	dirtyErr    error
	preserveErr error
	ref         string
	messages    []string
}

func (capture *tailCovCapture) DirtyPaths(_ context.Context, worktree string) ([]string, error) {
	if capture.dirtyErr != nil {
		return nil, capture.dirtyErr
	}
	return capture.dirty[worktree], nil
}

func (capture *tailCovCapture) Preserve(_ context.Context, _, message string) (string, error) {
	capture.messages = append(capture.messages, message)
	if capture.preserveErr != nil {
		return "", capture.preserveErr
	}
	return capture.ref, nil
}

// TestTailCovFailedReportsErrorsAndFailedMembers pins the three ways a
// result is a failure: an invocation-level error, a member that failed, and
// -- just as important -- that a clean run is not one.
func TestTailCovFailedReportsErrorsAndFailedMembers(t *testing.T) {
	if !(Result{Errors: []string{"inventory unavailable"}}).Failed() {
		t.Error("Failed() = false with an invocation error")
	}
	if !(Result{Members: []MemberResult{{Repository: "acme/app", Action: "failed"}}}).Failed() {
		t.Error("Failed() = false with a failed member")
	}
	if (Result{}).Failed() {
		t.Error("Failed() = true for an empty result")
	}
	clean := Result{
		Applied:      true,
		Members:      []MemberResult{{Repository: "acme/app", Action: "ended", Removed: true}},
		ClaimOutcome: "released",
	}
	if clean.Failed() {
		t.Errorf("Failed() = true for a fully retired run: %+v", clean)
	}
}

// TestTailCovRefusalErrorOmitsEmptySanctionList pins the refusal text both
// with and without a remediation command: a refusal that names no command
// must not render a dangling "; run:" clause.
func TestTailCovRefusalErrorOmitsEmptySanctionList(t *testing.T) {
	plain := &Refusal{Code: RefusalLiveLink, Message: "task still holds a live local link"}
	if got := plain.Error(); got != plain.Message {
		t.Fatalf("Error() = %q, want %q", got, plain.Message)
	}

	withCommands := &Refusal{
		Code:       RefusalLiveLink,
		Message:    "blocked",
		Sanctioned: []string{"wb deps propagate --undo", "wb deps unlink"},
	}
	want := "blocked; run: wb deps propagate --undo or wb deps unlink"
	if got := withCommands.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestTailCovCaptureMessageIdentifiesTaskInUTC covers the injected clock: the
// capture message is what an operator later greps in the stash history, so it
// must name the task and a UTC timestamp even when the clock reports another
// zone.
func TestTailCovCaptureMessageIdentifiesTaskInUTC(t *testing.T) {
	order := &[]string{}
	capture := &tailCovCapture{
		dirty: map[string][]string{"/wt/app": {"main.go"}},
		ref:   "refs/wb/capture/1",
	}
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory:    &tailCovInventory{worktrees: []Worktree{{Repository: "acme/app", Path: "/wt/app"}}},
		Capture:      capture,
		Retirer:      &fakeRetirer{err: map[string]error{}, order: order},
		Claims:       &fakeClaims{},
		Now: func() time.Time {
			return time.Date(2026, 9, 7, 12, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
		},
	}

	result, err := engine.End(context.Background(), Options{Task: "improve-login", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	want := "wb worktree end improve-login at 2026-09-07T10:00:00Z"
	if len(capture.messages) != 1 || capture.messages[0] != want {
		t.Fatalf("capture messages = %q, want [%q]", capture.messages, want)
	}
	if result.Members[0].CaptureRef != "refs/wb/capture/1" {
		t.Fatalf("member = %#v", result.Members[0])
	}
}

// TestTailCovEndRequiresTaskName pins that a blank task is refused before the
// inventory is even consulted: there is no "the current task" to end.
func TestTailCovEndRequiresTaskName(t *testing.T) {
	inventory := &tailCovInventory{worktrees: []Worktree{{Repository: "acme/app", Path: "/wt/app"}}}
	engine := &Engine{ProjectsRoot: "/projects", Inventory: inventory}

	result, err := engine.End(context.Background(), Options{Task: "   "})
	if err == nil {
		t.Fatal("ending a blank task succeeded")
	}
	if !strings.Contains(err.Error(), "a task name is required") {
		t.Fatalf("error = %v, want the task name refusal", err)
	}
	if inventory.calls != 0 {
		t.Fatalf("the inventory was consulted %d time(s) for a blank task", inventory.calls)
	}
	if len(result.Members) != 0 {
		t.Fatalf("result = %#v, want no members", result)
	}
}

// TestTailCovEndReportsInventoryFailure pins that a failed inventory listing
// aborts the whole invocation with that error and no partial result to act
// on.
func TestTailCovEndReportsInventoryFailure(t *testing.T) {
	boom := errors.New("inventory unavailable")
	capture := &tailCovCapture{}
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory:    &tailCovInventory{err: boom},
		Capture:      capture,
	}

	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if len(result.Members) != 0 {
		t.Fatalf("result = %#v, want no members", result)
	}
	if len(capture.messages) != 0 {
		t.Fatal("a failed inventory still captured work")
	}
}

// TestTailCovEndWithoutLinkGuardRetiresEveryWorktree covers the "no fence
// wired" path: the live-link check is skipped entirely, so a task whose
// worktrees are all clean ends with the claim released.
func TestTailCovEndWithoutLinkGuardRetiresEveryWorktree(t *testing.T) {
	retirer := &fakeRetirer{err: map[string]error{}, order: &[]string{}}
	claims := &fakeClaims{}
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory: &tailCovInventory{worktrees: []Worktree{
			{Repository: "acme/app", Path: "/wt/app"},
			{Repository: "acme/site", Path: "/wt/site"},
		}},
		Links:   nil,
		Capture: &tailCovCapture{dirty: map[string][]string{}},
		Retirer: retirer,
		Claims:  claims,
	}

	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if len(result.Members) != 2 || !result.Members[0].Removed || !result.Members[1].Removed {
		t.Fatalf("members = %#v, want both retired", result.Members)
	}
	if len(retirer.retired) != 2 || len(claims.released) != 1 || result.ClaimOutcome != "released" {
		t.Fatalf("retired=%v released=%v outcome=%q", retirer.retired, claims.released, result.ClaimOutcome)
	}
}

// TestTailCovEndReportsLinkGuardFailure pins that an unreadable link state is
// an error, not an assumed "no links": nothing is captured or removed, and
// the partial result names the task it was working on.
func TestTailCovEndReportsLinkGuardFailure(t *testing.T) {
	boom := errors.New("link state unreadable")
	capture := &tailCovCapture{dirty: map[string][]string{"/wt/app": {"main.go"}}}
	retirer := &fakeRetirer{err: map[string]error{}, order: &[]string{}}
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory:    &tailCovInventory{worktrees: []Worktree{{Repository: "acme/app", Path: "/wt/app"}}},
		Links:        tailCovLinks{err: boom},
		Capture:      capture,
		Retirer:      retirer,
		Claims:       &fakeClaims{},
	}

	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
	if result.Task != "t" || !result.Applied {
		t.Fatalf("result = %#v, want the attempted task reported", result)
	}
	if len(result.Members) != 0 || len(capture.messages) != 0 || len(retirer.retired) != 0 {
		t.Fatalf("a failed link check still acted: %#v", result)
	}
}

// TestTailCovEndReportsDirtyPathFailure pins that a worktree whose
// uncommitted state cannot even be listed is marked failed -- never
// "clean" -- and is not removed, so the claim is kept.
func TestTailCovEndReportsDirtyPathFailure(t *testing.T) {
	boom := errors.New("git status failed")
	capture := &tailCovCapture{dirtyErr: boom}
	retirer := &fakeRetirer{err: map[string]error{}, order: &[]string{}}
	claims := &fakeClaims{}
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory:    &tailCovInventory{worktrees: []Worktree{{Repository: "acme/app", Path: "/wt/app"}}},
		Capture:      capture,
		Retirer:      retirer,
		Claims:       claims,
	}

	result, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if !result.Failed() {
		t.Fatal("a worktree whose state could not be read was reported as success")
	}
	member := result.Members[0]
	if member.Action != "failed" || !strings.Contains(member.Detail, boom.Error()) {
		t.Fatalf("member = %#v, want a failure naming the cause", member)
	}
	if member.Removed || len(retirer.retired) != 0 {
		t.Fatalf("a worktree with unreadable state was removed: %#v", member)
	}
	if len(claims.released) != 0 || !strings.Contains(result.ClaimOutcome, "kept") {
		t.Fatalf("claim outcome = %q, want the claim kept", result.ClaimOutcome)
	}
}

// TestTailCovRefusalDeduplicatesAndSortsSanctionedCommands pins the refusal's
// remediation list: several worktrees can hold the same link, and the
// operator must be shown each distinct clearing command once, in a stable
// order.
func TestTailCovRefusalDeduplicatesAndSortsSanctionedCommands(t *testing.T) {
	engine := &Engine{
		ProjectsRoot: "/projects",
		Inventory: &tailCovInventory{worktrees: []Worktree{
			{Repository: "acme/app", Path: "/wt/app"},
			{Repository: "acme/site", Path: "/wt/site"},
		}},
		Links: tailCovLinks{
			reasons: map[string][]string{
				"/wt/app":  {"acme/app links a library working tree"},
				"/wt/site": {"acme/site links a library working tree"},
			},
			sanctioned: map[string][]string{
				"/wt/app":  {"wb deps unlink --worktree /wt/app", "wb deps propagate --undo"},
				"/wt/site": {"wb deps propagate --undo"},
			},
		},
		Capture: &tailCovCapture{},
	}

	_, err := engine.End(context.Background(), Options{Task: "t", Apply: true})
	refusal := &Refusal{}
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if refusal.Code != RefusalLiveLink {
		t.Fatalf("refusal code = %q, want %q", refusal.Code, RefusalLiveLink)
	}
	want := []string{"wb deps propagate --undo", "wb deps unlink --worktree /wt/app"}
	if len(refusal.Sanctioned) != len(want) {
		t.Fatalf("sanctioned = %v, want %v", refusal.Sanctioned, want)
	}
	for index := range want {
		if refusal.Sanctioned[index] != want[index] {
			t.Fatalf("sanctioned = %v, want %v", refusal.Sanctioned, want)
		}
	}
	if !strings.Contains(refusal.Message, "acme/app links a library working tree") ||
		!strings.Contains(refusal.Message, "acme/site links a library working tree") {
		t.Fatalf("refusal message does not name every live link: %q", refusal.Message)
	}
}

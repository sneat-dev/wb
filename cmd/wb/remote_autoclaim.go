package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// autoClaimStale is the staleness window used by the best-effort auto-claim
// path. It matches `wb remote claim`'s own default so a fleet-wide claim
// looks stale to the auto path exactly when it would to an operator running
// the explicit command by hand.
const autoClaimStale = 24 * time.Hour

// autoClaimResult is included in `worktree create --format json` output.
// Outcome is one of: disabled, skipped, acquired, refreshed, held,
// took_over. Detail carries the holder description for held/took_over and
// the failure reason for skipped; it is empty otherwise.
type autoClaimResult struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// tryAutoClaim best-effort claims task in the remote store before a
// worktree is created. It never returns an error and never blocks the
// caller: no `remote:` config is a silent no-op ("disabled"); a login,
// store, or provider failure is printed as "remote claim skipped: <why>"
// and swallowed. A fresh claim held by someone else is only warned about —
// "remote claim: task-X is held by <desc> — proceeding without the remote
// claim" — and left untouched (softened to "held by you on <machine>" for
// the same login on another machine, via holderDesc). A stale claim is
// taken over automatically via ClaimTakeOverStale, since a dead holder's
// machine is the one case auto-takeover is safe for; ClaimForce is never
// used by this path.
func tryAutoClaim(deps remoteDeps, projectsRoot, task string, stale time.Duration, out io.Writer) autoClaimResult {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		// Unconfigured (or any other config error) disables the feature
		// silently: worktree create must behave exactly as it did before
		// `wb remote` existed for any fleet that never opted in.
		return autoClaimResult{Outcome: "disabled"}
	}
	login, err := deps.login()
	if err != nil {
		return skippedAutoClaim(out, task, "determine GitHub login: "+err.Error())
	}
	if login == "" {
		return skippedAutoClaim(out, task, "determine GitHub login: empty login")
	}
	mine := remotestate.Claim{
		SchemaVersion: remotestate.ClaimSchemaVersion,
		Task:          task,
		Login:         login,
		Machine:       cfg.Machine,
		ClaimedAt:     deps.now(),
	}
	ctx := context.Background()
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimNormal, "")
	if err != nil {
		return skippedAutoClaim(out, task, err.Error())
	}
	switch outcome.Kind {
	case remotestate.ClaimAcquired:
		_, _ = fmt.Fprintf(out, "remote claim: acquired %s\n", task)
		return autoClaimResult{Outcome: "acquired"}
	case remotestate.ClaimRefreshed:
		_, _ = fmt.Fprintf(out, "remote claim: refreshed %s\n", task)
		return autoClaimResult{Outcome: "refreshed"}
	default: // remotestate.ClaimHeld
		return tryAutoTakeOverStale(ctx, provider, deps, mine, outcome.Current, stale, out)
	}
}

// tryAutoTakeOverStale judges the current holder's staleness and either
// takes over (stale) or warns and proceeds (fresh); any store error along
// the way is folded into "skipped", never propagated.
func tryAutoTakeOverStale(ctx context.Context, provider remotestate.Provider, deps remoteDeps, mine, holder remotestate.Claim, stale time.Duration, out io.Writer) autoClaimResult {
	machines, err := provider.List(ctx)
	if err != nil {
		return skippedAutoClaim(out, mine.Task, "read remote store: "+err.Error())
	}
	who := holderDesc(mine, holder)
	if !holderStale(machines, holder.Login, holder.Machine, deps.now(), stale) {
		_, _ = fmt.Fprintf(out, "remote claim: %s is held by %s — proceeding without the remote claim\n", mine.Task, who)
		return autoClaimResult{Outcome: "held", Detail: who}
	}
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimTakeOverStale, holder.Holder())
	if err != nil {
		return skippedAutoClaim(out, mine.Task, err.Error())
	}
	if outcome.Kind == remotestate.ClaimHeld {
		// The holder judged stale above already changed hands before this
		// call landed; behave like any other held-fresh claim and proceed
		// without it, naming the actual current holder.
		who = holderDesc(mine, outcome.Current)
		_, _ = fmt.Fprintf(out, "remote claim: %s is held by %s — proceeding without the remote claim\n", mine.Task, who)
		return autoClaimResult{Outcome: "held", Detail: who}
	}
	// Render from outcome.Previous, not the earlier-judged holder variable
	// (see the equivalent note in cmd/wb/remote_claim.go).
	prev := holder
	if outcome.Previous != nil {
		prev = *outcome.Previous
	}
	who = holderDesc(mine, prev)
	_, _ = fmt.Fprintf(out, "remote claim: took over %s from %s (stale)\n", mine.Task, who)
	return autoClaimResult{Outcome: "took_over", Detail: who}
}

func skippedAutoClaim(out io.Writer, task, detail string) autoClaimResult {
	_, _ = fmt.Fprintf(out, "remote claim skipped: %s\n", detail)
	return autoClaimResult{Outcome: "skipped", Detail: detail}
}

// autoReleaseResult reports what tryAutoRelease actually did, mirroring
// autoClaimResult. Outcome is one of: disabled, skipped, released, noop,
// held, failed.
//
// Every outcome except "failed" keeps tryAutoRelease's original contract:
// best-effort, silently (or advisory-)swallowed, never touching the host
// command's exit code. "failed" is the one wb#321 carved out — see Leaked.
type autoReleaseResult struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

// Leaked reports whether this release attempt could have left task's claim
// standing in the store after its worktree was already removed — the exact
// defect wb#321 reports. It is true only for "failed": provider.Release
// returned a transport/store error after gitrepo.Provider.Fetch's own
// bounded retries (see fetchRetryAttempts) were exhausted, which means the
// claim really was this login/machine's own and the store still could not
// be told to drop it. It is false for every advisory outcome — "disabled"
// and "skipped" (config/login trouble, so ownership was never even
// established) and "noop"/"held" (nothing was ours, or it is someone
// else's, and both are correctly left alone) — because none of those can
// strand a claim that was genuinely this machine's.
func (r autoReleaseResult) Leaked() bool { return r.Outcome == "failed" }

// tryAutoRelease best-effort releases this login/machine's own claim on
// task after a worktree is retired (cleanup --apply, or abort --apply with
// disposition discarded/handoff). No config, a login failure, and a claim
// held by someone else are all printed as "remote claim release skipped:
// <why>" and swallowed, exactly as before. force is never used — an
// auto-release must never remove a claim it does not own.
//
// A provider.Release error is different: gitrepo.Provider already retries
// the transient git failures wb#321 traced to two WB processes racing on
// the shared clone (see fetchRetryAttempts in internal/remotestate/gitrepo)
// and now also serializes the clone with cloneLock, so an error reaching
// here means those retries were exhausted, not merely lost a single race.
// The worktree this claim belonged to is already gone by the time this
// runs, so the claim genuinely was ours and now outlives it — that is a
// leak, not something to fold into the same "skipped" bucket as an
// unconfigured remote or someone else's claim. failedAutoRelease reports it
// distinctly (Outcome "failed", Leaked() true) so a caller can tell the two
// apart; see exitNonZeroOnReleaseLeak for what a caller does with that.
func tryAutoRelease(deps remoteDeps, projectsRoot, task string, out io.Writer) autoReleaseResult {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		// Unconfigured: silent, matching tryAutoClaim's own "disabled" path.
		return autoReleaseResult{Outcome: "disabled"}
	}
	login, err := deps.login()
	if err != nil {
		return skippedAutoRelease(out, "determine GitHub login: "+err.Error())
	}
	if login == "" {
		return skippedAutoRelease(out, "determine GitHub login: empty login")
	}
	outcome, err := provider.Release(context.Background(), task, login, cfg.Machine, false)
	if err != nil {
		return failedAutoRelease(out, task, err.Error())
	}
	switch outcome.Kind {
	case remotestate.Released:
		_, _ = fmt.Fprintf(out, "remote claim: released %s\n", task)
		return autoReleaseResult{Outcome: "released"}
	case remotestate.ReleaseNoop:
		// Nothing was ours to release; there is nothing worth telling the
		// operator about, so this stays silent like the "disabled" case.
		return autoReleaseResult{Outcome: "noop"}
	default: // remotestate.ReleaseHeldByOther
		if outcome.Current == nil {
			return skippedAutoRelease(out, "held by another machine")
		}
		mine := remotestate.Claim{Login: login, Machine: cfg.Machine}
		return skippedAutoRelease(out, "held by "+holderDesc(mine, *outcome.Current))
	}
}

func skippedAutoRelease(out io.Writer, detail string) autoReleaseResult {
	_, _ = fmt.Fprintf(out, "remote claim release skipped: %s\n", detail)
	return autoReleaseResult{Outcome: "skipped", Detail: detail}
}

// failedAutoRelease reports the one outcome tryAutoRelease's doc comment
// used to promise never happened: a claim that really was this
// login/machine's own, on a worktree that is already gone, left standing
// because the store still could not be written after retries. "FAILED" (not
// "skipped") and the task name up front are deliberate: this is the wb#321
// leak, and it must read differently under grep/log scanning from the
// merely advisory "remote claim release skipped:" lines above it.
func failedAutoRelease(out io.Writer, task, detail string) autoReleaseResult {
	_, _ = fmt.Fprintf(out, "remote claim release FAILED: %s claim was not released (its worktree is already gone): %s\n", task, detail)
	return autoReleaseResult{Outcome: "failed", Detail: detail}
}

// exitNonZeroOnReleaseLeak is wb#321's open decision, deliberately left
// unflipped by this change. The issue asks for a non-zero exit when a
// worktree removal succeeds but its claim release then fails — but
// tryAutoRelease's own doc comment (above, and historically) promises the
// opposite: release is best-effort and never changes the host command's
// exit code. That is a real contract change for `wb worktree cleanup` and
// `wb worktree abort`'s callers (e.g. a batch driver that currently treats
// exit 0 as "fully done"), so it is the repo owner's call, not this
// patch's. Flip this to true to make cleanup/abort return a non-zero exit
// for any task whose autoReleaseResult.Leaked() is true; the "remote claim
// release FAILED: <task> ..." line plus the leaked task name already let an
// operator (or `wb remote claims`) find and clear it either way.
const exitNonZeroOnReleaseLeak = false

// worktreeCreateAutoClaim is `worktree create`'s auto-claim hook, extracted
// so tests can call it directly with a fixture deps value instead of
// exercising the full command (which needs real repositories and prompt
// files). --no-claim short-circuits before deps is even consulted, so its
// JSON shape matches "disabled" exactly: no `remote:` config and an
// explicit --no-claim both mean "print the plain worktree results, nothing
// else."
func worktreeCreateAutoClaim(deps remoteDeps, noClaim bool, projectsRoot, task string, out io.Writer) autoClaimResult {
	if noClaim {
		return autoClaimResult{Outcome: "disabled"}
	}
	return tryAutoClaim(deps, projectsRoot, task, autoClaimStale, out)
}

// worktreeCreateJSON selects what `worktree create --format json` encodes.
// When no claim was attempted at all (outcome "disabled": no `remote:`
// config, or --no-claim), it is the plain worktree results array exactly as
// before this feature existed, so existing consumers see no change. Once a
// claim was actually attempted or skipped, the outcome travels alongside
// the results in a small wrapper instead.
func worktreeCreateJSON(claim autoClaimResult, results []worktrees.CreateResult) any {
	if claim.Outcome == "disabled" {
		return results
	}
	return struct {
		RemoteClaim autoClaimResult          `json:"remote_claim"`
		Worktrees   []worktrees.CreateResult `json:"worktrees"`
	}{RemoteClaim: claim, Worktrees: results}
}

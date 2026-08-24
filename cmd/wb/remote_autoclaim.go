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
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimNormal)
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
	if _, err := provider.Claim(ctx, mine, remotestate.ClaimTakeOverStale); err != nil {
		return skippedAutoClaim(out, mine.Task, err.Error())
	}
	_, _ = fmt.Fprintf(out, "remote claim: took over %s from %s (stale)\n", mine.Task, who)
	return autoClaimResult{Outcome: "took_over", Detail: who}
}

func skippedAutoClaim(out io.Writer, task, detail string) autoClaimResult {
	_, _ = fmt.Fprintf(out, "remote claim skipped: %s\n", detail)
	return autoClaimResult{Outcome: "skipped", Detail: detail}
}

// tryAutoRelease best-effort releases this login/machine's own claim on
// task after a worktree is retired (cleanup --apply, or abort --apply with
// disposition discarded/handoff). It is entirely best-effort: no config,
// a login/store failure, or a claim held by someone else are all printed as
// "remote claim release skipped: <why>" and swallowed. force is never used
// — an auto-release must never remove a claim it does not own. It never
// returns an error and never changes the host command's exit code.
func tryAutoRelease(deps remoteDeps, projectsRoot, task string, out io.Writer) {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		// Unconfigured: silent, matching tryAutoClaim's own "disabled" path.
		return
	}
	login, err := deps.login()
	if err != nil {
		skippedAutoRelease(out, "determine GitHub login: "+err.Error())
		return
	}
	if login == "" {
		skippedAutoRelease(out, "determine GitHub login: empty login")
		return
	}
	outcome, err := provider.Release(context.Background(), task, login, cfg.Machine, false)
	if err != nil {
		skippedAutoRelease(out, err.Error())
		return
	}
	switch outcome.Kind {
	case remotestate.Released:
		_, _ = fmt.Fprintf(out, "remote claim: released %s\n", task)
	case remotestate.ReleaseNoop:
		// Nothing was ours to release; there is nothing worth telling the
		// operator about, so this stays silent like the "disabled" case.
	default: // remotestate.ReleaseHeldByOther
		if outcome.Current == nil {
			skippedAutoRelease(out, "held by another machine")
		} else {
			mine := remotestate.Claim{Login: login, Machine: cfg.Machine}
			skippedAutoRelease(out, "held by "+holderDesc(mine, *outcome.Current))
		}
	}
}

func skippedAutoRelease(out io.Writer, detail string) {
	_, _ = fmt.Fprintf(out, "remote claim release skipped: %s\n", detail)
}

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

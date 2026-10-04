package remoterun

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/worktreerun"
)

func tryAutoClaim(deps Dependencies, projectsRoot, task string, stale time.Duration, out io.Writer) worktreerun.RemoteClaimOutcome {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		// Unconfigured (or any other config error) disables the feature
		// silently: worktree create must behave exactly as it did before
		// `wb remote` existed for any fleet that never opted in.
		return worktreerun.RemoteClaimOutcome{Outcome: "disabled"}
	}
	login, err := deps.Login()
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
		ClaimedAt:     deps.Now(),
	}
	ctx := context.Background()
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimNormal, "")
	if err != nil {
		return skippedAutoClaim(out, task, err.Error())
	}
	switch outcome.Kind {
	case remotestate.ClaimAcquired:
		_, _ = fmt.Fprintf(out, "remote claim: acquired %s\n", task)
		return worktreerun.RemoteClaimOutcome{Outcome: "acquired"}
	case remotestate.ClaimRefreshed:
		_, _ = fmt.Fprintf(out, "remote claim: refreshed %s\n", task)
		return worktreerun.RemoteClaimOutcome{Outcome: "refreshed"}
	default: // remotestate.ClaimHeld
		return tryAutoTakeOverStale(ctx, provider, deps, mine, outcome.Current, stale, out)
	}
}

func tryAutoTakeOverStale(ctx context.Context, provider remotestate.Provider, deps Dependencies, mine, holder remotestate.Claim, stale time.Duration, out io.Writer) worktreerun.RemoteClaimOutcome {
	machines, err := provider.List(ctx)
	if err != nil {
		return skippedAutoClaim(out, mine.Task, "read remote store: "+err.Error())
	}
	who := HolderDesc(mine, holder)
	if !HolderStale(machines, holder.Login, holder.Machine, deps.Now(), stale) {
		_, _ = fmt.Fprintf(out, "remote claim: %s is held by %s — proceeding without the remote claim\n", mine.Task, who)
		return worktreerun.RemoteClaimOutcome{Outcome: "held", Detail: who}
	}
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimTakeOverStale, holder.Holder())
	if err != nil {
		return skippedAutoClaim(out, mine.Task, err.Error())
	}
	if outcome.Kind == remotestate.ClaimHeld {
		// The holder judged stale above already changed hands before this
		// call landed; behave like any other held-fresh claim and proceed
		// without it, naming the actual current holder.
		who = HolderDesc(mine, outcome.Current)
		_, _ = fmt.Fprintf(out, "remote claim: %s is held by %s — proceeding without the remote claim\n", mine.Task, who)
		return worktreerun.RemoteClaimOutcome{Outcome: "held", Detail: who}
	}
	// Render from outcome.Previous, not the earlier-judged holder variable
	// (see the equivalent note in cmd/wb/remote_claim.go).
	prev := holder
	if outcome.Previous != nil {
		prev = *outcome.Previous
	}
	who = HolderDesc(mine, prev)
	_, _ = fmt.Fprintf(out, "remote claim: took over %s from %s (stale)\n", mine.Task, who)
	return worktreerun.RemoteClaimOutcome{Outcome: "took_over", Detail: who}
}

func tryAutoRelease(deps Dependencies, projectsRoot, task string, out io.Writer) ReleaseAdvisory {
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		// Unconfigured: silent, matching tryAutoClaim's own "disabled" path.
		return ReleaseAdvisory{Outcome: "disabled"}
	}
	login, err := deps.Login()
	if err != nil {
		return SkippedAutoRelease(out, "determine GitHub login: "+err.Error())
	}
	if login == "" {
		return SkippedAutoRelease(out, "determine GitHub login: empty login")
	}
	outcome, err := provider.Release(context.Background(), task, login, cfg.Machine, false)
	if err != nil {
		return FailedAutoRelease(out, task, err.Error())
	}
	switch outcome.Kind {
	case remotestate.Released:
		_, _ = fmt.Fprintf(out, "remote claim: released %s\n", task)
		return ReleaseAdvisory{Outcome: "released"}
	case remotestate.ReleaseNoop:
		// Nothing was ours to release; there is nothing worth telling the
		// operator about, so this stays silent like the "disabled" case.
		return ReleaseAdvisory{Outcome: "noop"}
	default: // remotestate.ReleaseHeldByOther
		if outcome.Current == nil {
			return SkippedAutoRelease(out, "held by another machine")
		}
		mine := remotestate.Claim{Login: login, Machine: cfg.Machine}
		return SkippedAutoRelease(out, "held by "+HolderDesc(mine, *outcome.Current))
	}
}

func skippedAutoClaim(out io.Writer, task, detail string) worktreerun.RemoteClaimOutcome {
	_, _ = fmt.Fprintf(out, "remote claim skipped: %s\n", detail)
	return worktreerun.RemoteClaimOutcome{Outcome: "skipped", Detail: detail}
}

func SkippedAutoRelease(out io.Writer, detail string) ReleaseAdvisory {
	_, _ = fmt.Fprintf(out, "remote claim release skipped: %s\n", detail)
	return ReleaseAdvisory{Outcome: "skipped", Detail: detail}
}

func FailedAutoRelease(out io.Writer, task, detail string) ReleaseAdvisory {
	_, _ = fmt.Fprintf(out, "remote claim release FAILED: %s claim was not released (its worktree is already gone): %s\n", task, detail)
	return ReleaseAdvisory{Outcome: "failed", Detail: detail}
}

type ReleaseAdvisory struct {
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

func (r ReleaseAdvisory) Leaked() bool { return r.Outcome == "failed" }
func (s *Service) AutoClaim(root, task string, stale time.Duration, out io.Writer) worktreerun.RemoteClaimOutcome {
	return tryAutoClaim(s.deps, root, task, stale, out)
}
func (s *Service) AutoRelease(root, task string, out io.Writer) ReleaseAdvisory {
	return tryAutoRelease(s.deps, root, task, out)
}

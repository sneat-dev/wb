package locallink

import (
	"context"
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
)

// Verification is one consumer's verification against the linked copy.
type Verification struct {
	// Statement is the sentence every run under a live link must print, so a
	// local result is never mistaken for a published-dependency result.
	Statement string `json:"statement"`
	// ActiveLinks names the links in effect, the published version each
	// replaced, and the content hash verified against, so a result can be tied
	// to an exact library tree after the fact.
	ActiveLinks []string        `json:"active_links"`
	Linked      VerificationRun `json:"linked"`
	// PublishedBaseline is the GOWORK=off build and vet: the pre-landing check
	// proving the consumer still resolves its published dependency.
	PublishedBaseline VerificationRun `json:"published_baseline"`
	Passed            bool            `json:"passed"`
}

// verifyConsumers runs every linked consumer's own lint and tests against the
// linked copy, single-worker, and reports per consumer.
//
// It does not stop at the first failure: the point of a stream is to learn
// about every consumer in one pass.
//
// Implements: dependency-streams#req:verify-runs-single-worker-against-the-linked-copy,
// dependency-streams#req:the-local-gate-states-what-it-verified-against,
// dependency-streams#req:verification-prints-its-active-links.
func (engine *Engine) verifyConsumers(ctx context.Context, options Options, result *Result) {
	if engine.Verifier == nil {
		for index := range result.Consumers {
			// A consumer that was skipped or never linked was not part of the
			// run, so telling it a verifier was unavailable reports a failure
			// for work that was never attempted.
			if result.Consumers[index].Skipped || len(result.Consumers[index].Links) == 0 {
				continue
			}
			result.Consumers[index].Errors = append(result.Consumers[index].Errors, "no verifier available")
		}
		return
	}
	for index := range result.Consumers {
		consumer := &result.Consumers[index]
		if consumer.Skipped || len(consumer.Links) == 0 {
			continue
		}
		verification := Verification{
			Statement:   LocalGateStatement(result.libraryName(), result.ContentHash, result.Dirty),
			ActiveLinks: renderActiveLinks(consumer.Links),
		}
		run, err := engine.Verifier.Verify(ctx, consumer.Consumer, quality.SingleWorkerNodeEnv())
		if err != nil {
			consumer.Errors = append(consumer.Errors, err.Error())
		}
		verification.Linked = run
		baseline, err := engine.Verifier.BuildAndVet(ctx, consumer.Consumer)
		if err != nil {
			consumer.Errors = append(consumer.Errors, err.Error())
		}
		verification.PublishedBaseline = baseline
		verification.Passed = run.Passed && baseline.Passed
		consumer.Verification = &verification
	}
}

func (result Result) libraryName() string {
	if result.LibraryRepository != "" {
		return result.LibraryRepository
	}
	return result.Library
}

// LocalGateStatement is the sentence a verification run under a live link must
// print. Locally, every `go` invocation from the worktree root down discovers
// the `go.work` and therefore verifies against an unpublished library; that is
// the point of the link, and it must never be mistaken for a
// published-dependency result.
func LocalGateStatement(library, hash string, dirty bool) string {
	statement := fmt.Sprintf("verified against unpublished %s at content-hash %s", library, hash)
	if dirty {
		return statement + " (dirty)"
	}
	return statement + " (clean)"
}

func renderActiveLinks(links []streams.Link) []string {
	rendered := make([]string, 0, len(links))
	for _, link := range links {
		previous := link.PreviousVersion
		if previous == "" {
			previous = "(none declared)"
		}
		rendered = append(rendered, fmt.Sprintf("%s via %s replaces %s, content-hash %s",
			link.Identity, link.Mechanism, previous, link.ContentHash))
	}
	return rendered
}

// QualityVerifier runs a consumer's own `wb verify` profiles, constrained to a
// single worker. It is the production Verifier: reusing the existing profiles
// is what keeps a local gate executing the same mechanisms CI does, rather than
// a second, divergent runner.
type QualityVerifier struct {
	Options quality.RunOptions
}

// Verify implements Verifier.
func (verifier QualityVerifier) Verify(ctx context.Context, dir string, env []string) (VerificationRun, error) {
	options := verifier.Options
	options.SingleWorker = true
	options.Env = append(append([]string(nil), options.Env...), env...)
	report := quality.VerifyWithOptions(ctx, dir, dir, []quality.Check{quality.CheckLint, quality.CheckTest}, options)
	return summarize(report), nil
}

// BuildAndVet implements Verifier. GOWORK=off is set by the verb itself rather
// than left to the caller, because a workspace the toolchain discovers is
// exactly what this check exists to exclude.
func (verifier QualityVerifier) BuildAndVet(ctx context.Context, dir string) (VerificationRun, error) {
	options := verifier.Options
	options.SingleWorker = true
	options.Env = append(append([]string(nil), options.Env...), "GOWORK=off")
	report := quality.VerifyWithOptions(ctx, dir, dir, []quality.Check{quality.CheckBuild, quality.CheckLint}, options)
	run := summarize(report)
	run.Command = "GOWORK=off " + run.Command
	return run, nil
}

func summarize(report quality.VerificationReport) VerificationRun {
	run := VerificationRun{Passed: report.Status != quality.StatusFailed}
	commands := make([]string, 0, len(report.Results))
	for _, entry := range report.Results {
		if entry.Status == quality.StatusSkipped {
			continue
		}
		commands = append(commands, entry.Command)
		if entry.Status == quality.StatusFailed {
			run.Details = append(run.Details, fmt.Sprintf("%s %s: %s", entry.Module, entry.Check, entry.Detail))
		}
	}
	run.Command = strings.Join(dedupe(commands), "; ")
	return run
}

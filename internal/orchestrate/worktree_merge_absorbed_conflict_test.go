package orchestrate

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestAcknowledgeAbsorbedConflictProvesAncestorAndContentAbsorbedSourcesAndFreesLane(t *testing.T) {
	fixture := newEngineFixture(t)
	ancestorSource := createMergeSourceOnBase(t, fixture, "task-ancestor", "feature/ancestor", "main", "ancestor.txt", "ancestor\n")
	contentSource := createMergeSourceOnBase(t, fixture, "task-content", "feature/content", "main", "content.txt", "content\n")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{ancestorSource.WorktreeDir, contentSource.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	receipt.Failure = "source changed during prepare: recovery fixture"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", receipt.Sources[0].SHA)
	writeEngineFile(t, fixture.canonical+"/content.txt", "content\n")
	runEngineGit(t, fixture.canonical, "add", "content.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: land content independently")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	if err := os.RemoveAll(ancestorSource.WorktreeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(contentSource.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	dryRun, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "absorbed_conflict_acknowledged" || len(dryRun.SourceProofs) != 2 {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if dryRun.SourceProofs[0].Method != "ancestor" {
		t.Fatalf("ancestor source proof = %+v", dryRun.SourceProofs[0])
	}
	if dryRun.SourceProofs[1].Method != "content_absorbed" || dryRun.SourceProofs[1].MergeBaseSHA == "" || dryRun.SourceProofs[1].PathCount != 1 {
		t.Fatalf("content-absorbed source proof = %+v", dryRun.SourceProofs[1])
	}
	if _, statErr := os.Stat(absorbedConflictAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run wrote acknowledgement: %v", statErr)
	}

	ack, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "audited absorbed conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.AcknowledgementPath == "" {
		t.Fatalf("applied acknowledgement lacks identity: %+v", ack)
	}
	if unchanged, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || string(unchanged) != string(original) {
		t.Fatalf("historical merge receipt was rewritten: err=%v", readErr)
	}
	if _, statErr := os.Stat(ack.AcknowledgementPath); statErr != nil {
		t.Fatalf("acknowledgement missing: %v", statErr)
	}
	// Candidate worktree must survive: this verb never deletes it.
	if _, statErr := os.Stat(receipt.Candidate.Worktree); statErr != nil {
		t.Fatalf("candidate worktree removed: %v", statErr)
	}

	ackAgain, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "retry",
	})
	if err != nil || ackAgain.ID != ack.ID {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}

	// Site 1: the exact receipt is now recognized as acknowledged. A real
	// exact-source retry cannot be exercised here because the sources are
	// deliberately gone (that is the whole point of this receipt shape); the
	// production reuse loop consults exactly this predicate before it would
	// otherwise try to resume against the missing worktrees.
	if acknowledged, ackErr := hasAbsorbedConflictAcknowledgement(receipt); ackErr != nil || !acknowledged {
		t.Fatalf("hasAbsorbedConflictAcknowledgement = %v, %v", acknowledged, ackErr)
	}

	// Site 2: a distinct source set proceeds past the lane pre-flight, proving
	// activeWorktreeMergeLaneReceipt now treats the acknowledged receipt as
	// terminal rather than "still owned by non-terminal receipt".
	freshSource := createMergeSourceOnBase(t, fixture, "task-fresh", "feature/fresh", "main", "fresh.txt", "fresh\n")
	freed, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{freshSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("prepare after acknowledged absorbed conflict: %v", err)
	}
	if freed.Status != WorktreeMergePrepared {
		t.Fatalf("freed lane prepare = %+v", freed)
	}
}

func TestAcknowledgeAbsorbedConflictRefusals(t *testing.T) {
	t.Run("source worktree still exists", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSourceOnBase(t, fixture, "task-a", "feature/a", "main", "a.txt", "a\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeConflict
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		// Source worktree deliberately left in place.
		if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		}); err == nil || !strings.Contains(err.Error(), "still exists") {
			t.Fatalf("error = %v, want source-still-exists refusal", err)
		}
	})

	t.Run("source content diverges from target", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSourceOnBase(t, fixture, "task-b", "feature/b", "main", "b.txt", "b\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeConflict
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		// Advance target with unrelated content that never reproduces b.txt.
		writeEngineFile(t, fixture.canonical+"/unrelated.txt", "unrelated\n")
		runEngineGit(t, fixture.canonical, "add", "unrelated.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "chore: unrelated")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		if err := os.RemoveAll(source.WorktreeDir); err != nil {
			t.Fatal(err)
		}
		_, err = AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		})
		if err == nil || !strings.Contains(err.Error(), "not content-absorbed") {
			t.Fatalf("error = %v, want not-content-absorbed refusal", err)
		}
	})

	t.Run("published candidate refuses", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSourceOnBase(t, fixture, "task-c", "feature/c", "main", "c.txt", "c\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeConflict
		receipt.PullRequest = "https://example.test/acme/app/pull/1"
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		}); err == nil || !strings.Contains(err.Error(), "acknowledge-stranded-landing") {
			t.Fatalf("error = %v, want published-candidate refusal", err)
		}
	})

	t.Run("wrong status refuses", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSourceOnBase(t, fixture, "task-d", "feature/d", "main", "d.txt", "d\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		// Receipt is prepared, not conflict.
		if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		}); err == nil || !strings.Contains(err.Error(), "want an unpublished prepare conflict") {
			t.Fatalf("error = %v, want wrong-status refusal", err)
		}
	})

	t.Run("apply without actor or reason refuses", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSourceOnBase(t, fixture, "task-e", "feature/e", "main", "e.txt", "e\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeConflict
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "fetch", "origin")
		runEngineGit(t, fixture.canonical, "checkout", "main")
		runEngineGit(t, fixture.canonical, "merge", "--ff-only", receipt.Sources[0].SHA)
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		if err := os.RemoveAll(source.WorktreeDir); err != nil {
			t.Fatal(err)
		}
		if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
		}); err == nil || !strings.Contains(err.Error(), "--actor and --reason are required") {
			t.Fatalf("error = %v, want actor/reason refusal", err)
		}
	})
}

func TestAcknowledgeAbsorbedConflictTamperedAcknowledgementLeavesLaneBlocked(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-f", "feature/f", "main", "f.txt", "f\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	runEngineGit(t, fixture.canonical, "merge", "--ff-only", receipt.Sources[0].SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	ack, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "audited absorbed conflict",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Tamper the sidecar: flip the recorded actor without recomputing ID.
	tampered, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	tamperedContents := strings.Replace(string(tampered), "\"reviewer\"", "\"attacker\"", 1)
	if tamperedContents == string(tampered) {
		t.Fatal("tamper substitution did not change the acknowledgement")
	}
	if err := os.WriteFile(ack.AcknowledgementPath, []byte(tamperedContents), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := hasAbsorbedConflictAcknowledgement(receipt); err == nil {
		t.Fatal("tampered acknowledgement was accepted as valid")
	}

	// The lane stays blocked: an exact-source retry still fails, now on the
	// tampered-evidence error rather than a clean "acknowledged" refusal, and
	// never silently proceeds as if the lane had been freed.
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil {
		t.Fatal("prepare succeeded despite tampered absorbed-conflict acknowledgement")
	}
}

func TestAcknowledgeAbsorbedConflictLinesAbsorbedJSONLPath(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-jsonl", "feature/jsonl", "main", ".specscore/events.jsonl", "source-1\nsource-2\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// Advance the target independently with a differently ordered ledger that
	// still carries every line the source added, plus lines of its own -- the
	// append-only ledger shape this proof targets.
	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	writeEngineFile(t, fixture.canonical+"/.specscore/events.jsonl", "other-0\nsource-1\nsource-2\nother-3\n")
	runEngineGit(t, fixture.canonical, "add", ".specscore/events.jsonl")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: land ledger entries independently")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	ack, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ack.SourceProofs) != 1 || ack.SourceProofs[0].Method != "content_absorbed" {
		t.Fatalf("source proofs = %+v", ack.SourceProofs)
	}
	pathProofs := ack.SourceProofs[0].PathProofs
	if len(pathProofs) != 1 || pathProofs[0].Path != ".specscore/events.jsonl" || pathProofs[0].Method != "lines_absorbed" ||
		pathProofs[0].AddedLines != 2 || pathProofs[0].MatchedLines != 2 {
		t.Fatalf("path proofs = %+v", pathProofs)
	}
}

func TestAcknowledgeAbsorbedConflictLinesAbsorbedRefusesMissingLine(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "task-jsonl-missing", "feature/jsonl-missing", "main", ".specscore/events.jsonl", "source-1\nsource-2\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// The target's independent ledger carries only one of the two lines the
	// source added.
	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	writeEngineFile(t, fixture.canonical+"/.specscore/events.jsonl", "source-1\nother-1\n")
	runEngineGit(t, fixture.canonical, "add", ".specscore/events.jsonl")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: land partial ledger entries")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	}); err == nil || !strings.Contains(err.Error(), "not lines-absorbed") {
		t.Fatalf("error = %v, want not-lines-absorbed refusal", err)
	}
}

// specIndexReadme renders a spec/**/README.md that satisfies SpecScore's own
// `specscore spec lint` structural conventions, so PrepareWorktreeMerge's
// candidate validation neither introduces nor changes a lint failure when a
// test fixture touches one -- exactly the generated-index shape this recovery
// excuses via --derived-path.
func specIndexReadme(title, formatURL, body string) string {
	return "---\nformat: " + formatURL + "\n---\n\n# " + title + "\n\n" + body + "\n\n## Open Questions\n\nNone.\n\n---\nAdherence: " + formatURL + "\n"
}

func seedSpecScoreBaseline(t *testing.T, fixture engineFixture) {
	t.Helper()
	writeEngineFile(t, fixture.canonical+"/specscore.yaml", "version: 1\n")
	writeEngineFile(t, fixture.canonical+"/spec/README.md", "# Spec\n\nIndex.\n\n## Open Questions\n\nNone.\n")
	runEngineGit(t, fixture.canonical, "add", "specscore.yaml", "spec/README.md")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: seed specscore baseline")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
}

func TestAcknowledgeAbsorbedConflictDerivedPathExcusesGeneratedIndex(t *testing.T) {
	fixture := newEngineFixture(t)
	seedSpecScoreBaseline(t, fixture)
	lessonsFormat := "https://specscore.md/lessons-index-specification"
	source := createMergeSourceOnBase(t, fixture, "task-derived", "feature/derived", "main", "spec/lessons/README.md", specIndexReadme("Lessons", lessonsFormat, "Index v1."))
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}

	// The target regenerated the same index independently, with different
	// bytes -- exactly the shape a generated spec/**/README.md listing takes.
	runEngineGit(t, fixture.canonical, "fetch", "origin")
	runEngineGit(t, fixture.canonical, "checkout", "main")
	writeEngineFile(t, fixture.canonical+"/spec/lessons/README.md", specIndexReadme("Lessons", lessonsFormat, "Index v2 (regenerated)."))
	runEngineGit(t, fixture.canonical, "add", "spec/lessons/README.md")
	runEngineGit(t, fixture.canonical, "commit", "-m", "chore: regenerate spec/lessons index")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	if err := os.RemoveAll(source.WorktreeDir); err != nil {
		t.Fatal(err)
	}

	ack, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, DerivedPaths: []string{"spec/lessons/README.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ack.ExcusedDerivedPaths, "spec/lessons/README.md") {
		t.Fatalf("excused derived paths = %+v", ack.ExcusedDerivedPaths)
	}
	pathProofs := ack.SourceProofs[0].PathProofs
	if len(pathProofs) != 1 || pathProofs[0].Path != "spec/lessons/README.md" || pathProofs[0].Method != "derived_excused" {
		t.Fatalf("path proofs = %+v", pathProofs)
	}

	// Applying and re-reading proves the sidecar records the excused path and
	// the operator reason together, and that the record round-trips.
	applied, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer",
		Reason: "audited regenerated spec/lessons index", DerivedPaths: []string{"spec/lessons/README.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied.Reason != "audited regenerated spec/lessons index" || !slices.Contains(applied.ExcusedDerivedPaths, "spec/lessons/README.md") {
		t.Fatalf("applied acknowledgement = %+v", applied)
	}
	if acknowledged, ackErr := hasAbsorbedConflictAcknowledgement(receipt); ackErr != nil || !acknowledged {
		t.Fatalf("hasAbsorbedConflictAcknowledgement = %v, %v", acknowledged, ackErr)
	}
}

func TestAcknowledgeAbsorbedConflictDerivedPathRefusals(t *testing.T) {
	t.Run("non-allowlisted shape refuses", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSourceOnBase(t, fixture, "task-derived-bad-shape", "feature/derived-bad-shape", "main", "docs/README.md", "index-v1\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeConflict
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "fetch", "origin")
		runEngineGit(t, fixture.canonical, "checkout", "main")
		writeEngineFile(t, fixture.canonical+"/docs/README.md", "index-v2\n")
		runEngineGit(t, fixture.canonical, "add", "docs/README.md")
		runEngineGit(t, fixture.canonical, "commit", "-m", "chore: regenerate docs index")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		if err := os.RemoveAll(source.WorktreeDir); err != nil {
			t.Fatal(err)
		}
		if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, DerivedPaths: []string{"docs/README.md"},
		}); err == nil || !strings.Contains(err.Error(), "not an allowed derived-index shape") {
			t.Fatalf("error = %v, want disallowed-shape refusal", err)
		}
	})

	t.Run("path absent on target refuses", func(t *testing.T) {
		fixture := newEngineFixture(t)
		seedSpecScoreBaseline(t, fixture)
		plansFormat := "https://specscore.md/plans-index-specification"
		source := createMergeSourceOnBase(t, fixture, "task-derived-absent", "feature/derived-absent", "main", "spec/plans/README.md", specIndexReadme("Plans", plansFormat, "Index v1."))
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
			ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeConflict
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		// Target never gains spec/plans/README.md at all.
		runEngineGit(t, fixture.canonical, "fetch", "origin")
		runEngineGit(t, fixture.canonical, "checkout", "main")
		writeEngineFile(t, fixture.canonical+"/unrelated.txt", "unrelated\n")
		runEngineGit(t, fixture.canonical, "add", "unrelated.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "chore: unrelated")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		if err := os.RemoveAll(source.WorktreeDir); err != nil {
			t.Fatal(err)
		}
		if _, err := AcknowledgeAbsorbedConflict(context.Background(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, DerivedPaths: []string{"spec/plans/README.md"},
		}); err == nil || !strings.Contains(err.Error(), "does not exist on the current target") {
			t.Fatalf("error = %v, want absent-on-target refusal", err)
		}
	})
}

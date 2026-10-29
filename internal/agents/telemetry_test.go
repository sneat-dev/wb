package agents

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTaskIdentityIsStableAndCountsBytes(t *testing.T) {
	t.Parallel()
	digest, size := TaskIdentity("do the thing")
	if len(digest) != 64 || size != len("do the thing") {
		t.Fatalf("identity = %q %d", digest, size)
	}
	again, _ := TaskIdentity("do the thing")
	if again != digest {
		t.Fatal("the task hash must be stable")
	}
	other, _ := TaskIdentity("do the thing ")
	if other == digest {
		t.Fatal("different bytes must have a different hash")
	}
}

func TestRedactingWriterRemovesASecretSplitAcrossWrites(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	writer := newRedactingWriter(&sink, "sk-secret-value")
	if writer == nil {
		t.Fatal("a non-empty secret must produce a writer")
	}
	chunks := []string{"before sk-se", "cret-va", "lue after"}
	for _, chunk := range chunks {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.String(), "sk-secret-value") {
		t.Fatalf("the secret reached the sink: %q", sink.String())
	}
	if !strings.Contains(sink.String(), "before [redacted] after") {
		t.Fatalf("sink = %q", sink.String())
	}
	if writer.replaced != 1 {
		t.Fatalf("replacements = %d", writer.replaced)
	}
}

func TestRedactingWriterPassesShortTailAndNothingWhenUnset(t *testing.T) {
	t.Parallel()
	var sink bytes.Buffer
	writer := newRedactingWriter(&sink, "secret")
	if _, err := writer.Write([]byte("se")); err != nil {
		t.Fatal(err)
	}
	if sink.Len() != 0 {
		t.Fatalf("a partial match must be held back: %q", sink.String())
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "se" {
		t.Fatalf("sink = %q", sink.String())
	}
	var plain bytes.Buffer
	if newRedactingWriter(&plain, "  ") != nil {
		t.Fatal("an empty secret must disable redaction")
	}
	if _, err := (&redactingWriter{inner: &plain}).Write([]byte("raw")); err != nil {
		t.Fatal(err)
	}
	if plain.String() != "raw" {
		t.Fatalf("plain sink = %q", plain.String())
	}
	if err := (&redactingWriter{inner: &plain}).Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestRedactTextHandlesTheHarnessFinalMessage(t *testing.T) {
	t.Parallel()
	text, count := RedactText("the key is sk-1 and sk-1", "sk-1")
	if count != 2 || strings.Contains(text, "sk-1") {
		t.Fatalf("text = %q count=%d", text, count)
	}
	if unchanged, count := RedactText("nothing", ""); unchanged != "nothing" || count != 0 {
		t.Fatalf("empty secret = %q %d", unchanged, count)
	}
}

func TestBuildTelemetryNeverFabricatesUsage(t *testing.T) {
	t.Parallel()
	missing := buildTelemetry(HarnessCodex, HarnessSummary{})
	if missing.Available || missing.Source != UsageUnavailable || missing.UnavailableReason == "" {
		t.Fatalf("missing usage = %#v", missing)
	}
	dsh := buildTelemetry(HarnessDSH, HarnessSummary{})
	if !strings.Contains(dsh.UnavailableReason, "final message") {
		t.Fatalf("dsh reason = %q", dsh.UnavailableReason)
	}
	tokens := &Tokens{Input: intPointer(10), Output: intPointer(3)}
	present := buildTelemetry(HarnessCodex, HarnessSummary{Tokens: tokens})
	if !present.Available || present.Tokens != tokens || len(present.Raw) == 0 {
		t.Fatalf("present usage = %#v", present)
	}
}

func TestBuildCostLabelsProvenanceAndRefusesAFreeSubscriptionRun(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	unavailable := buildCost(HarnessCodex, HarnessSummary{}, now)
	if unavailable.USD != nil || unavailable.Source != CostUnavailable || unavailable.Note == "" {
		t.Fatalf("unavailable cost = %#v", unavailable)
	}
	note := buildCost(HarnessOpenCode, HarnessSummary{CostNote: "zero with tokens"}, now)
	if note.USD != nil || note.Note != "zero with tokens" {
		t.Fatalf("noted cost = %#v", note)
	}
	cost := 0.25
	priced := buildCost(HarnessOpenCode, HarnessSummary{CostUSD: &cost}, now)
	if priced.USD == nil || *priced.USD != cost || priced.Kind != "api_equivalent" || priced.Source != CostHarnessEstimated {
		t.Fatalf("priced cost = %#v", priced)
	}
	if priced.PricingVersion == "" || priced.EstimatedAt.IsZero() {
		t.Fatalf("provenance = %#v", priced)
	}
}

func TestLegacyUsageOnlyCarriesObservedValues(t *testing.T) {
	t.Parallel()
	if legacyUsage(nil) != nil {
		t.Fatal("no tokens must yield no legacy usage")
	}
	if legacyUsage(&Tokens{}) != nil {
		t.Fatal("tokens with no observed counts must yield no legacy usage")
	}
	usage := legacyUsage(&Tokens{
		Input: intPointer(10), Output: intPointer(2), CacheRead: intPointer(3),
		CacheWrite: intPointer(4), Reasoning: intPointer(5),
	})
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 2 || usage.CachedInputTokens != 3 || usage.CacheWriteInputTokens != 4 || usage.ReasoningOutputTokens != 5 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestFirstWriteClockRecordsOnlyTheFirstOutput(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0)
	record := &Record{StartedAt: now}
	clock := &firstWriteClock{now: func() time.Time { return now.Add(1500 * time.Millisecond) }, started: now}
	writer := &timedWriter{inner: &bytes.Buffer{}, clock: clock}
	if _, err := writer.Write(nil); err != nil {
		t.Fatal(err)
	}
	if _, observed := clock.elapsedMS(); observed {
		t.Fatal("an empty write is not output")
	}
	_, _ = writer.Write([]byte("first"))
	clock.now = func() time.Time { return now.Add(5 * time.Second) }
	_, _ = writer.Write([]byte("second"))
	// The clock never mutates the record: only the owner's accessor copy does.
	if record.TimeToFirstOutputMS != 0 {
		t.Fatalf("the clock mutated the record directly: %d", record.TimeToFirstOutputMS)
	}
	elapsed, observed := clock.elapsedMS()
	if !observed || elapsed != 1500 {
		t.Fatalf("time to first output = %d (observed %t), want 1500", elapsed, observed)
	}
	record.TimeToFirstOutputMS = elapsed
	if record.TimeToFirstOutputMS != 1500 {
		t.Fatalf("time to first output = %d", record.TimeToFirstOutputMS)
	}
	// A clock with no start records nothing and reports no observation.
	stopped := &firstWriteClock{now: func() time.Time { return now }, started: time.Time{}}
	stopped.mark()
	if _, observed := stopped.elapsedMS(); observed {
		t.Fatal("a clock without a start must report no observation")
	}
}

func TestFirstWriteClockKeepsItsValuePrivateUnderConcurrentWrites(t *testing.T) {
	t.Parallel()
	now := time.Unix(2000, 0)
	clock := &firstWriteClock{now: func() time.Time { return now.Add(250 * time.Millisecond) }, started: now}
	var writers sync.WaitGroup
	for index := 0; index < 8; index++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, _ = (&timedWriter{inner: &bytes.Buffer{}, clock: clock}).Write([]byte("output"))
		}()
	}
	writers.Wait()
	if elapsed, observed := clock.elapsedMS(); !observed || elapsed != 250 {
		t.Fatalf("time to first output = %d (observed %t), want 250", elapsed, observed)
	}
}

func TestExecutionStatusVocabularyIsClosed(t *testing.T) {
	t.Parallel()
	for status, terminal := range map[ExecutionStatus]bool{
		StatusQueued: false, StatusRunning: false, StatusCompleted: true,
		StatusFailed: true, StatusCancelled: true, StatusTimedOut: true, StatusAbandoned: true,
	} {
		if got := status.Terminal(); got != terminal {
			t.Fatalf("%s.Terminal() = %v", status, got)
		}
	}
	for state, status := range map[State]ExecutionStatus{
		StateCompleted: StatusCompleted, StateFailed: StatusFailed,
		StateTimeout: StatusTimedOut, StateAbandoned: StatusAbandoned, StateRunning: StatusRunning,
	} {
		if got := executionStatusFromState(state); got != status {
			t.Fatalf("executionStatusFromState(%s) = %s", state, got)
		}
	}
}

func TestNormalizeStateDerivesTheAdditiveStatus(t *testing.T) {
	t.Parallel()
	NormalizeState(nil)
	terminal := &Result{State: StateCompleted}
	NormalizeState(terminal)
	if !terminal.Terminal || terminal.Status != StatusCompleted {
		t.Fatalf("normalized = %#v", terminal)
	}
	running := &Result{State: StateRunning}
	NormalizeState(running)
	if running.Terminal || running.Status != StatusRunning {
		t.Fatalf("running = %#v", running)
	}
}

func TestRedactingWriterRemovesASecretAtEverySplitPoint(t *testing.T) {
	t.Parallel()
	const secret = "sk-secret-value"
	const text = "before sk-secret-value after sk-secret-value"
	for split := 0; split <= len(text); split++ {
		var sink bytes.Buffer
		writer := newRedactingWriter(&sink, secret)
		if writer == nil {
			t.Fatal("a non-empty secret must produce a writer")
		}
		if _, err := writer.Write([]byte(text[:split])); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(text[split:])); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(sink.String(), secret) {
			t.Fatalf("split %d leaked the secret: %q", split, sink.String())
		}
		if strings.Count(sink.String(), redactionMarker) != 2 {
			t.Fatalf("split %d produced %q", split, sink.String())
		}
		if !strings.HasPrefix(sink.String(), "before ") || !strings.HasSuffix(sink.String(), " after [redacted]") {
			t.Fatalf("split %d changed the surrounding text: %q", split, sink.String())
		}
	}
}

func TestRedactingWriterHandlesASecretAtTheStartOfEveryWrite(t *testing.T) {
	t.Parallel()
	const secret = "abc"
	var sink bytes.Buffer
	writer := newRedactingWriter(&sink, secret)
	for _, chunk := range []string{"abc", "abcabc", "xabc", "abcx", "ab", "c"} {
		if _, err := writer.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.String(), secret) {
		t.Fatalf("sink leaked: %q", sink.String())
	}
	// Six complete occurrences across the six writes: "abc", two in "abcabc",
	// one in "xabc", one in "abcx", and the one completed by the final "c".
	if strings.Count(sink.String(), redactionMarker) != 6 {
		t.Fatalf("sink = %q", sink.String())
	}
}

func TestRedactingWriterHandlesASecretThatSharesItsFirstAndLastByte(t *testing.T) {
	t.Parallel()
	// "abca" and "abab" are the shapes that used to leak: a complete match can
	// end with a fragment that is also a prefix of the secret.
	for _, secret := range []string{"abca", "abab", "aaaa"} {
		text := "x" + secret + "y" + secret + secret + "z"
		for split := 0; split <= len(text); split++ {
			var sink bytes.Buffer
			writer := newRedactingWriter(&sink, secret)
			if _, err := writer.Write([]byte(text[:split])); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write([]byte(text[split:])); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(sink.String(), secret) {
				t.Fatalf("secret %q split %d leaked: %q", secret, split, sink.String())
			}
		}
	}
}

func TestRedactingWriterNeverLeaksAnOverlappingSecretAtASplit(t *testing.T) {
	t.Parallel()
	// A secret that can overlap itself ("abab" in "ababab") is the shape where a
	// greedy replacement leaves a fragment that belongs to the next match.
	const secret = "abab"
	text := "x" + secret + "y" + secret + secret + "ab"
	for split := 0; split <= len(text); split++ {
		var sink bytes.Buffer
		writer := newRedactingWriter(&sink, secret)
		_, _ = writer.Write([]byte(text[:split]))
		_, _ = writer.Write([]byte(text[split:]))
		_ = writer.Flush()
		if strings.Contains(sink.String(), secret) {
			t.Fatalf("split %d leaked: %q", split, sink.String())
		}
	}
}

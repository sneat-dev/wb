package agents

import (
	"strings"
	"testing"
	"time"
)

func TestContainedLifetimeAddsSetupRunValidationAndCleanupBudgets(t *testing.T) {
	t.Parallel()
	steps := []ValidationStep{
		{Name: "unit tests", Argv: []string{"go", "test", "./..."}, Timeout: "5m"},
		{Name: "lint", Argv: []string{"go", "vet", "./..."}},
	}
	got, err := ContainedLifetime(time.Hour.Milliseconds(), steps)
	if err != nil {
		t.Fatalf("ContainedLifetime: %v", err)
	}
	want := time.Hour + 5*time.Minute + defaultValidationStepTimeout + containerSetupBudget + containerCleanupBudget
	if got != want {
		t.Fatalf("lifetime = %s, want %s", got, want)
	}
	if got > maxContainerLifetime {
		t.Fatalf("lifetime %s exceeds the maximum %s", got, maxContainerLifetime)
	}
}

func TestContainedLifetimeKeepsSetupAndCleanupAsSeparateFinitePools(t *testing.T) {
	t.Parallel()
	got, err := ContainedLifetime(time.Minute.Milliseconds(), nil)
	if err != nil {
		t.Fatalf("ContainedLifetime: %v", err)
	}
	allowance := got - time.Minute
	if allowance != containerSetupBudget+containerCleanupBudget {
		t.Fatalf("non-run allowance = %s, want setup %s plus cleanup %s", allowance, containerSetupBudget, containerCleanupBudget)
	}
	if containerCleanupBudget <= 0 || containerCleanupBudget != containerCleanupTimeout+containerAttachReapTimeout+containerAttachKillTimeout {
		t.Fatalf("cleanup budget = %s, want the bounded cleanup, reap and kill windows", containerCleanupBudget)
	}
	if allowance >= maxContainerLifetime {
		t.Fatalf("setup plus cleanup %s must stay below the maximum %s or no run bound could fit", allowance, maxContainerLifetime)
	}
}

func TestContainedLifetimeAcceptsASlowLegitimateSetupWithoutShrinkingTheRun(t *testing.T) {
	t.Parallel()
	// A three-and-a-half hour run plus a quarter-hour validation still fits
	// under the four-hour ceiling once the explicit setup and cleanup pools are
	// accounted for; a slow setup must come out of its own pool, never out of
	// the declared harness or validation time.
	got, err := ContainedLifetime((3*time.Hour + 30*time.Minute).Milliseconds(), []ValidationStep{
		{Name: "slow but legitimate", Argv: []string{"true"}, Timeout: "15m"},
	})
	if err != nil {
		t.Fatalf("ContainedLifetime: %v", err)
	}
	want := 3*time.Hour + 30*time.Minute + 15*time.Minute + containerSetupBudget + containerCleanupBudget
	if got != want {
		t.Fatalf("lifetime = %s, want %s", got, want)
	}
	if got > maxContainerLifetime {
		t.Fatalf("lifetime %s exceeds the maximum %s", got, maxContainerLifetime)
	}
	// A longer run would exceed the ceiling once validation, setup and cleanup
	// are accounted for: the refusal must be about the complete total rather
	// than silently trimming a stage.
	if _, err := ContainedLifetime((3*time.Hour + 45*time.Minute).Milliseconds(), []ValidationStep{
		{Name: "slow but legitimate", Argv: []string{"true"}, Timeout: "15m"},
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want an excess refusal", err)
	}
}

func TestContainedLifetimeDefaultsEveryValidationStep(t *testing.T) {
	t.Parallel()
	steps := []ValidationStep{
		{Name: "one", Argv: []string{"true"}},
		{Name: "two", Argv: []string{"true"}, Timeout: ""},
	}
	got, err := ContainedLifetime(time.Minute.Milliseconds(), steps)
	if err != nil {
		t.Fatalf("ContainedLifetime: %v", err)
	}
	want := time.Minute + 2*defaultValidationStepTimeout + containerSetupBudget + containerCleanupBudget
	if got != want {
		t.Fatalf("lifetime = %s, want %s", got, want)
	}
}

func TestContainedLifetimeRefusesUnboundedOrOverflowingRunTimeout(t *testing.T) {
	t.Parallel()
	const maxInt64 = int64(1<<63 - 1)
	maxRepresentableMilliseconds := int64(time.Duration(1<<63-1) / time.Millisecond)
	cases := map[string]struct {
		timeoutMS int64
		want      string
	}{
		"zero":                   {timeoutMS: 0, want: "positive run timeout"},
		"negative":               {timeoutMS: -1, want: "positive run timeout"},
		"max int64":              {timeoutMS: maxInt64, want: "cannot be represented"},
		"one past representable": {timeoutMS: maxRepresentableMilliseconds + 1, want: "cannot be represented"},
		"exactly four hours":     {timeoutMS: int64(maxContainerLifetime / time.Millisecond), want: "leaves no room"},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ContainedLifetime(testCase.timeoutMS, nil); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestContainedLifetimeRefusesValidationExcessInsteadOfShrinkingIt(t *testing.T) {
	t.Parallel()
	runTimeout := time.Hour.Milliseconds()
	if _, err := ContainedLifetime(runTimeout, []ValidationStep{
		{Name: "too long", Argv: []string{"true"}, Timeout: "4h"},
	}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("a validation step above the maximum must be refused: %v", err)
	}
	if _, err := ContainedLifetime(runTimeout, []ValidationStep{
		{Name: "malformed", Argv: []string{"true"}, Timeout: "not-a-duration"},
	}); err == nil || !strings.Contains(err.Error(), "not a Go duration") {
		t.Fatalf("a malformed validation bound must be refused: %v", err)
	}
	// Many individually valid bounds that together exceed the maximum must be
	// refused rather than summed into an overflow.
	steps := make([]ValidationStep, 100000)
	for index := range steps {
		steps[index] = ValidationStep{Name: "step", Argv: []string{"true"}, Timeout: "1h"}
	}
	if got, err := ContainedLifetime(runTimeout, steps); err == nil {
		t.Fatalf("excess validation budgets were accepted as %s", got)
	} else if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want an excess refusal", err)
	}
}

func TestContainedLifetimeAcceptsACombinedTotalBelowTheMaximum(t *testing.T) {
	t.Parallel()
	got, err := ContainedLifetime(time.Hour.Milliseconds(), []ValidationStep{
		{Name: "two hours", Argv: []string{"true"}, Timeout: "2h"},
	})
	if err != nil {
		t.Fatalf("ContainedLifetime: %v", err)
	}
	want := time.Hour + 2*time.Hour + containerSetupBudget + containerCleanupBudget
	if got != want {
		t.Fatalf("lifetime = %s, want %s", got, want)
	}
}

func TestContainedRecordLifetimeUsesThePersistedBoundAndSnapshottedSteps(t *testing.T) {
	t.Parallel()
	record := Record{
		TimeoutMS: time.Hour.Milliseconds(),
		Validation: ValidationRecord{Steps: []ValidationStepResult{
			{Name: "unit", Timeout: "2m"},
			{Name: "lint"},
		}},
	}
	got, err := ContainedRecordLifetime(record)
	if err != nil {
		t.Fatalf("ContainedRecordLifetime: %v", err)
	}
	want := time.Hour + 2*time.Minute + defaultValidationStepTimeout + containerSetupBudget + containerCleanupBudget
	if got != want {
		t.Fatalf("lifetime = %s, want %s", got, want)
	}
	record.TimeoutMS = 0
	if _, err := ContainedRecordLifetime(record); err == nil {
		t.Fatal("a record without a positive run bound must be refused")
	}
}

func TestValidationBudgetKeepsItsLegacyHostSemantics(t *testing.T) {
	t.Parallel()
	budget := ValidationBudget([]ValidationStepResult{
		{Timeout: "2m"},
		{Timeout: ""},
		{Timeout: "not-a-duration"},
	})
	if budget != 2*time.Minute+2*defaultValidationStepTimeout {
		t.Fatalf("legacy validation budget = %s", budget)
	}
}

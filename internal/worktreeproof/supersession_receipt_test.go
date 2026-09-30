package worktreeproof

import (
	"bytes"
	"strings"
	"testing"
)

func TestSupersessionReceiptAuditPreservesJSONAndOrdersCopy(t *testing.T) {
	t.Parallel()
	receipt := SupersessionReceipt{OriginalPR: "https://example.test/pull/3", OriginalPRNumber: 3,
		OriginalPRRepository: "acme/app", OriginalPRHead: "source", OriginalHead: "original", TargetHead: "target",
		DependencyDeltasComplete: true,
		DependencyDeltas: []SupersessionDependencyDelta{
			{SourcePR: "z", Consumer: "z", Package: "pkg-z", RequestedAfter: "2"},
			{SourcePR: "a", Consumer: "a", Package: "pkg-a", RequestedAfter: "1"},
		},
	}
	data, err := receipt.DependencyAuditJSON()
	if err != nil || bytes.Index(data, []byte(`"source_pr": "a"`)) > bytes.Index(data, []byte(`"source_pr": "z"`)) {
		t.Fatalf("sorted audit JSON = %s, %v", data, err)
	}
	markdown := receipt.DependencyAuditMarkdown()
	if !strings.Contains(markdown, "# Dependency supersession audit:") || strings.Index(markdown, "pkg-a") > strings.Index(markdown, "pkg-z") {
		t.Fatalf("sorted audit Markdown = %q", markdown)
	}
	if receipt.DependencyDeltas[0].SourcePR != "z" {
		t.Fatal("rendering mutated immutable receipt")
	}
	if _, err := (&SupersessionReceipt{}).DependencyAuditJSON(); err != nil {
		t.Fatal(err)
	}
	if got := (&SupersessionReceipt{}).DependencyAuditMarkdown(); !strings.Contains(got, "Complete: `false`") {
		t.Fatalf("empty audit = %q", got)
	}
}

func TestSupersessionDependencyDeltaOrderUsesEveryIdentityKey(t *testing.T) {
	t.Parallel()
	keys := []struct {
		name string
		set  func(*SupersessionDependencyDelta, string)
	}{
		{"source PR", func(d *SupersessionDependencyDelta, v string) { d.SourcePR = v }},
		{"consumer", func(d *SupersessionDependencyDelta, v string) { d.Consumer = v }},
		{"manifest", func(d *SupersessionDependencyDelta, v string) { d.Manifest = v }},
		{"selector", func(d *SupersessionDependencyDelta, v string) { d.Selector = v }},
		{"package", func(d *SupersessionDependencyDelta, v string) { d.Package = v }},
		{"before", func(d *SupersessionDependencyDelta, v string) { d.Before = v }},
		{"requested after", func(d *SupersessionDependencyDelta, v string) { d.RequestedAfter = v }},
	}
	for _, key := range keys {
		key := key
		t.Run(key.name, func(t *testing.T) {
			t.Parallel()
			left, right := SupersessionDependencyDelta{}, SupersessionDependencyDelta{}
			key.set(&left, "a")
			key.set(&right, "z")
			sorted := SortedDependencyDeltas([]SupersessionDependencyDelta{right, left})
			if sorted[0] != left || sorted[1] != right {
				t.Fatalf("%s sort = %#v", key.name, sorted)
			}
		})
	}
}

func TestSupersessionReceiptEvidenceEquality(t *testing.T) {
	t.Parallel()
	left := &SupersessionReceipt{Version: 1, Task: "task"}
	if !SameSupersessionReceipt(nil, nil) || SameSupersessionReceipt(left, nil) || SameSupersessionReceipt(nil, left) ||
		!SameSupersessionReceipt(left, &SupersessionReceipt{Version: 1, Task: "task"}) ||
		SameSupersessionReceipt(left, &SupersessionReceipt{Version: 1, Task: "other"}) {
		t.Fatal("immutable receipt equality")
	}
}

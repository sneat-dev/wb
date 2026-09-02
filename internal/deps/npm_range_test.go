package deps

import (
	"strings"
	"testing"
)

func TestNpmRangeAdmitsEvaluatesTheSupportedSubset(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		specifier string
		version   string
		evaluated bool
		admits    bool
	}{
		{name: "empty accepts anything", specifier: "", version: "1.2.3", evaluated: true, admits: true},
		{name: "star accepts anything", specifier: "*", version: "9.9.9", evaluated: true, admits: true},
		{name: "exact match", specifier: "0.14.0", version: "0.14.0", evaluated: true, admits: true},
		{name: "exact pin rejects a newer patch", specifier: "0.14.0", version: "0.14.3", evaluated: true, admits: false},
		{name: "equals operator", specifier: "=1.2.3", version: "1.2.3", evaluated: true, admits: true},
		{name: "caret on 1.x reaches the next minor", specifier: "^1.2.3", version: "1.9.0", evaluated: true, admits: true},
		{name: "caret on 1.x stops at the next major", specifier: "^1.2.3", version: "2.0.0", evaluated: true, admits: false},
		{name: "caret on 0.x reaches the next patch", specifier: "^0.24.1", version: "0.24.9", evaluated: true, admits: true},
		{name: "caret on 0.x stops at the next minor", specifier: "^0.24.1", version: "0.25.0", evaluated: true, admits: false},
		{name: "caret on 0.0.x stops at the next patch", specifier: "^0.0.3", version: "0.0.4", evaluated: true, admits: false},
		{name: "caret rejects an older version", specifier: "^1.2.3", version: "1.2.2", evaluated: true, admits: false},
		{name: "tilde allows patches", specifier: "~1.2.3", version: "1.2.9", evaluated: true, admits: true},
		{name: "tilde stops at the next minor", specifier: "~1.2.3", version: "1.3.0", evaluated: true, admits: false},
		{name: "greater or equal", specifier: ">=1.2.3", version: "4.0.0", evaluated: true, admits: true},
		{name: "greater or equal at the boundary", specifier: ">=1.2.3", version: "1.2.3", evaluated: true, admits: true},
		{name: "strictly greater at the boundary", specifier: ">1.2.3", version: "1.2.3", evaluated: true, admits: false},
		{name: "less than", specifier: "<2.0.0", version: "1.9.9", evaluated: true, admits: true},
		{name: "less or equal at the boundary", specifier: "<=1.2.3", version: "1.2.3", evaluated: true, admits: true},
		// A space-separated conjunction is how npm pins a major line, and it is
		// the shape every Angular and Ionic peerDependency in this fleet uses.
		{name: "conjunction pins a major line", specifier: ">=22.0.0 <23.0.0", version: "22.1.4", evaluated: true, admits: true},
		{name: "conjunction rejects the next major", specifier: ">=22.0.0 <23.0.0", version: "23.0.0", evaluated: true, admits: false},
		{name: "conjunction rejects below its floor", specifier: ">=22.0.0 <23.0.0", version: "21.9.9", evaluated: true, admits: false},
		{name: "conjunction of caret and upper bound", specifier: "^1.2.0 <1.9.0", version: "1.5.0", evaluated: true, admits: true},
		{name: "union admits its second branch", specifier: "1.0.0||2.0.0", version: "2.0.0", evaluated: true, admits: true},
		{name: "union rejects a version no branch admits", specifier: "^1.0.0 || ^2.0.0", version: "3.0.0", evaluated: true, admits: false},
		{name: "union of conjunctions", specifier: ">=15.0.0 <16.0.0 || >=22.0.0 <23.0.0", version: "22.1.4", evaluated: true, admits: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			verdict := npmRangeAdmits(testCase.specifier, testCase.version)
			if verdict.Evaluated != testCase.evaluated || verdict.Admits != testCase.admits {
				t.Fatalf("npmRangeAdmits(%q, %q) = %+v, want evaluated=%v admits=%v",
					testCase.specifier, testCase.version, verdict, testCase.evaluated, testCase.admits)
			}
		})
	}
}

func TestNpmRangeAdmitsRefusesToGuessUnsupportedShapes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name      string
		specifier string
		version   string
	}{
		{name: "workspace protocol", specifier: "workspace:*", version: "1.0.0"},
		{name: "catalog protocol", specifier: "catalog:default", version: "1.0.0"},
		{name: "npm alias", specifier: "npm:@other/name@1.0.0", version: "1.0.0"},
		{name: "file protocol", specifier: "file:../local", version: "1.0.0"},
		{name: "hyphen range", specifier: "1.0.0 - 2.0.0", version: "1.5.0"},
		{name: "wildcard minor", specifier: "1.x", version: "1.5.0"},
		{name: "prerelease specifier", specifier: "^1.0.0-beta.1", version: "1.0.0"},
		{name: "prerelease candidate", specifier: "^1.0.0", version: "1.1.0-rc.1"},
		{name: "candidate is not a version", specifier: "^1.0.0", version: "latest"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			verdict := npmRangeAdmits(testCase.specifier, testCase.version)
			if verdict.Evaluated {
				t.Fatalf("npmRangeAdmits(%q, %q) = %+v, want an unevaluated verdict", testCase.specifier, testCase.version, verdict)
			}
			if verdict.Reason == "" {
				t.Fatalf("npmRangeAdmits(%q, %q) returned no reason", testCase.specifier, testCase.version)
			}
		})
	}
}

func TestNpmCaretAndTildeCeilings(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct{ literal, caret, tilde string }{
		{literal: "1.2.3", caret: "2.0.0", tilde: "1.3.0"},
		{literal: "0.24.1", caret: "0.25.0", tilde: "0.25.0"},
		{literal: "0.0.3", caret: "0.0.4", tilde: "0.1.0"},
	} {
		if got := npmCaretCeiling(testCase.literal); got != testCase.caret {
			t.Fatalf("npmCaretCeiling(%q) = %q, want %q", testCase.literal, got, testCase.caret)
		}
		if got := npmTildeCeiling(testCase.literal); got != testCase.tilde {
			t.Fatalf("npmTildeCeiling(%q) = %q, want %q", testCase.literal, got, testCase.tilde)
		}
	}
}

// The two asymmetries are the whole safety argument for evaluating compound
// ranges at all. In each direction, one readable comparator settles the
// question; otherwise WB reports that it could not read the range rather than
// guessing — and it never guesses in the direction that hides a conflict.
func TestNpmCompoundRangesNeverGuessInTheUnsafeDirection(t *testing.T) {
	t.Parallel()

	// A conjunction: one readable comparator that REJECTS is decisive, because
	// AND means every comparator must hold.
	if verdict := npmRangeAdmits(">=22.0.0 <23.0.0-weird.x", "21.0.0"); !verdict.Evaluated || verdict.Admits {
		t.Fatalf("a readable rejecting comparator must settle a conjunction: %+v", verdict)
	}
	// The same conjunction with a version its readable comparator accepts
	// cannot be answered: the unreadable comparator might still reject.
	verdict := npmRangeAdmits(">=22.0.0 <23.0.0-weird.x", "99.0.0")
	if verdict.Evaluated || verdict.Reason == "" {
		t.Fatalf("an unreadable comparator must leave a conjunction unevaluated: %+v", verdict)
	}

	// A union: one readable branch that ADMITS is decisive, because OR means
	// any branch may hold.
	if verdict := npmRangeAdmits("^1.0.0 || 2.x", "1.5.0"); !verdict.Evaluated || !verdict.Admits {
		t.Fatalf("a readable admitting branch must settle a union: %+v", verdict)
	}
	// The same union with a version its readable branch rejects cannot be
	// answered: the unreadable branch might still have admitted it.
	verdict = npmRangeAdmits("^1.0.0 || 2.x", "2.5.0")
	if verdict.Evaluated || verdict.Reason == "" {
		t.Fatalf("an unreadable branch must leave a union unevaluated: %+v", verdict)
	}
}

// Hyphen ranges and comma lists stay declined, each with its own reason: a
// hyphen range is a distinct grammar rather than a conjunction, and npm ranges
// never use commas, so a comma is more likely a manifest written for another
// ecosystem than a range WB should interpret.
func TestNpmRangeStillDeclinesHyphenAndCommaShapes(t *testing.T) {
	t.Parallel()
	hyphen := npmRangeAdmits("1.0.0 - 2.0.0", "1.5.0")
	if hyphen.Evaluated || !strings.Contains(hyphen.Reason, "hyphen range") {
		t.Fatalf("hyphen verdict = %+v", hyphen)
	}
	comma := npmRangeAdmits(">=1.0.0, <2.0.0", "1.5.0")
	if comma.Evaluated || !strings.Contains(comma.Reason, "comma-separated") {
		t.Fatalf("comma verdict = %+v", comma)
	}
}

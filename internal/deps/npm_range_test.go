package deps

import "testing"

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
		{name: "union", specifier: "1.0.0||2.0.0", version: "2.0.0"},
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

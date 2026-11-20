package mechanicalchange

import (
	"testing"
)

// M2: the classification reads the diff, not the filename. A `package.json`
// holds the scripts CI runs and the overrides that rewrite the whole graph.
func TestMechanicalIsDecidedFromContent(t *testing.T) {
	t.Parallel()
	versionOnly := `@@ -5,7 +5,7 @@
   "dependencies": {
-    "lodash": "^4.17.20"
+    "lodash": "^4.17.21"
   }`
	scriptsOnly := `@@ -2,7 +2,7 @@
   "scripts": {
-    "build": "tsc"
+    "build": "tsc && node scripts/postbuild.js"
   }`
	overrides := `@@ -9,7 +9,7 @@
   "pnpm": {
     "overrides": {
-      "semver": "7.5.4"
+      "semver": "7.6.0"
     }`
	for _, testCase := range []struct {
		name  string
		files []ChangedFile
		want  bool
	}{
		{"a version-only manifest edit", []ChangedFile{{Filename: "package.json", Patch: versionOnly}}, true},
		{"go.mod and go.sum alone", []ChangedFile{
			{Filename: "go.mod", Patch: "@@\n-require x v1\n+require x v2\n"},
			{Filename: "go.sum", Patch: "@@\n-x v1 h1:a=\n+x v2 h1:b=\n"},
		}, true},
		{"a scripts edit inside a manifest", []ChangedFile{{Filename: "package.json", Patch: scriptsOnly}}, false},
		{"a pnpm override version pin", []ChangedFile{{Filename: "package.json", Patch: overrides}}, true},
		{"a manifest under testdata", []ChangedFile{{Filename: "internal/x/testdata/package.json", Patch: versionOnly}}, false},
		{"a manifest beside a source file", []ChangedFile{
			{Filename: "go.mod", Patch: "@@\n-require x v1\n+require x v2\n"},
			{Filename: "main.go", Patch: "@@\n-a\n+b\n"},
		}, false},
		{"a manifest GitHub could not diff", []ChangedFile{{Filename: "pnpm-lock.yaml", Patch: ""}}, false},
		{"no files at all", nil, false},
		// The sneat-apps#3494 shape: the only context is in the hunk header, so
		// skipping it left the section stack empty and a real bump was refused.
		{"a bump whose section is only in the hunk header", []ChangedFile{{
			Filename: "package.json",
			Patch: "@@ -12,7 +12,7 @@   \"dependencies\": {\n" +
				"     \"@sneat/core\": \"0.68.0\",\n" +
				"-    \"@sneat/extensions\": \"0.38.3\",\n" +
				"+    \"@sneat/extensions\": \"0.38.4\",\n" +
				"     \"rxjs\": \"7.8.1\"",
		}}, true},
		// Dependency pins are a version bump wherever the manager keeps them.
		{"an npm overrides block", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -20,7 +20,7 @@   \"overrides\": {\n-    \"semver\": \"7.5.4\"\n+    \"semver\": \"7.6.0\"",
		}}, true},
		{"a yarn resolutions block", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -20,7 +20,7 @@   \"resolutions\": {\n-    \"semver\": \"7.5.4\"\n+    \"semver\": \"7.6.0\"",
		}}, true},
		// Everything else that rewrites the graph is still a decision.
		{"a packageExtensions edit", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -20,7 +20,7 @@   \"packageExtensions\": {\n-    \"semver\": \"7.5.4\"\n+    \"semver\": \"7.6.0\"",
		}}, false},
		{"an override rewritten to a script", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -20,7 +20,7 @@   \"overrides\": {\n-    \"semver\": \"7.5.4\"\n+    \"semver\": \"node evil.js\"",
		}}, false},
		{"a workflow file", []ChangedFile{{
			Filename: ".github/workflows/release.yml",
			Patch:    "@@ -1 +1 @@\n-version: v2.1.0\n+version: v2.2.0",
		}}, false},
		{"a go.mod replace directive", []ChangedFile{{
			Filename: "go.mod",
			Patch:    "@@ -8,3 +8,3 @@\n-require github.com/acme/lib v1.2.0\n+require github.com/acme/lib v1.3.0\n+replace github.com/acme/lib => ../lib",
		}}, false},
		{"a go directive bump", []ChangedFile{{
			Filename: "go.mod",
			Patch:    "@@ -3,1 +3,1 @@\n-go 1.24\n+go 1.25",
		}}, false},
		{"a pnpm-workspace overrides block", []ChangedFile{{
			Filename: "pnpm-workspace.yaml",
			Patch:    "@@ -1,4 +1,4 @@\n overrides:\n-  semver: 7.5.4\n+  semver: 7.6.0",
		}}, true},
		{"a pnpm-workspace packageExtensions block", []ChangedFile{{
			Filename: "pnpm-workspace.yaml",
			Patch:    "@@ -1,4 +1,4 @@ packageExtensions:\n-  semver: 7.5.4\n+  semver: 7.6.0",
		}}, false},
		{"a deep package.json bump when the head content places the hunk", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -4,5 +4,5 @@\n     \"b\": \"1.0.0\",\n-    \"c\": \"1.0.0\",\n+    \"c\": \"1.0.1\",\n     \"d\": \"1.0.0\"\n   }",
			Content:  "{\n  \"name\": \"x\",\n  \"dependencies\": {\n    \"a\": \"1.0.0\",\n    \"b\": \"1.0.0\",\n    \"c\": \"1.0.1\",\n    \"d\": \"1.0.0\"\n  }\n}\n",
		}}, true},
		{"the same deep hunk without head content cannot be placed", []ChangedFile{{
			Filename: "package.json",
			Patch:    "@@ -4,5 +4,5 @@\n     \"b\": \"1.0.0\",\n-    \"c\": \"1.0.0\",\n+    \"c\": \"1.0.1\",\n     \"d\": \"1.0.0\"\n   }",
		}}, false},
		{"a plain go.mod require bump", []ChangedFile{{
			Filename: "go.mod",
			Patch:    "@@ -8,1 +8,1 @@\n-\tgithub.com/acme/lib v1.2.0\n+\tgithub.com/acme/lib v1.3.0",
		}}, true},
	} {
		verdict := ClassifyMechanical(testCase.files)
		if verdict.Mechanical != testCase.want {
			t.Errorf("%s: mechanical = %t, want %t (%s)", testCase.name, verdict.Mechanical, testCase.want, verdict.Summary())
		}
	}
}

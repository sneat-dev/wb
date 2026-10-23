package deps

import (
	"reflect"
	"testing"
)

func TestImmutableManifestScansSelectOnlyMatchedLines(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, input, want string
		workspace         bool
		count             int
	}{
		{name: "JSON matching line", input: "{\r\n  \"dependencies\": {\r\n    \"sdk\": \"1.0.0\",\r\n    \"other\": \"2.0.0\"\r\n  }\r\n}\r\n", want: "{\r\n  \"dependencies\": {\r\n    \"sdk\": \"3.0.0\",\r\n    \"other\": \"2.0.0\"\r\n  }\r\n}\r\n", count: 1},
		{name: "JSON malformed value", input: "{\n  \"dependencies\": {\n    \"sdk\": unquoted\n  }\n}\n", want: "{\n  \"dependencies\": {\n    \"sdk\": unquoted\n  }\n}\n"},
		{name: "catalog dedent", workspace: true, input: "catalogs:\n  default:\n    sdk: '1.0.0' # keep\n  other:\n    other: 2.0.0\nnext: value\n", want: "catalogs:\n  default:\n    sdk: '3.0.0' # keep\n  other:\n    other: 2.0.0\nnext: value\n", count: 1},
		{name: "catalog malformed key", workspace: true, input: "catalogs:\n  default:\n    'sdk: 1.0.0\n", want: "catalogs:\n  default:\n    'sdk: 1.0.0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var got []byte
			var count int
			var err error
			original := []byte(test.input)
			before := append([]byte(nil), original...)
			if test.workspace {
				var refs []pnpmWorkspaceRef
				got, refs, err = applyPnpmWorkspaceOverride(original, "sdk", "3.0.0")
				count = len(refs)
			} else {
				var refs []npmPackageJSONRef
				got, refs, err = applyNpmPackageJSONOverride(original, "sdk", "3.0.0")
				count = len(refs)
			}
			if err != nil || string(got) != test.want || count != test.count || !reflect.DeepEqual(before, original) {
				t.Fatalf("output=%q count=%d err=%v original=%q", got, count, err, original)
			}
		})
	}
}

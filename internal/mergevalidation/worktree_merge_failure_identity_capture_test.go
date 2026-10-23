package mergevalidation

import (
	"reflect"
	"testing"
)

func TestFailureIdentityOwnerNormalizedUnicodeAndInvalidUTF8Captures(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name, input string
		want        map[string]struct{}
	}{
		{"Unicode separator and identity", "spec/界.md:9\u00a0rule-α:\u2003value", map[string]struct{}{"spec/界.md rule-α": {}}},
		{"invalid UTF8 identity bytes", "spec/\xff.md:2 rule-\xfe: value", map[string]struct{}{"spec/\xff.md rule-\xfe": {}}},
		{"whitespace-only path", "\u2003:3 rule-x: value", map[string]struct{}{}},
		{"whitespace-only rule", "spec/a.md:3\u2003\u00a0:\tvalue", map[string]struct{}{}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			got := specScoreViolationIdentities(row.input)
			if !reflect.DeepEqual(got, row.want) {
				t.Fatalf("native parser identities=%q want %q", got, row.want)
			}
		})
	}
}

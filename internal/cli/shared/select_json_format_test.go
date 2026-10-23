package shared

import (
	"strings"
	"testing"
)

func TestSelectJSONFormatPreservesIndependentFlagPolicy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		format string
		json   bool
		want   string
	}{{"text", false, "text"}, {"json", false, "json"}, {"text", true, "json"}, {"json", true, "json"}} {
		got, err := SelectJSONFormat(tt.format, tt.json)
		if err != nil || got != tt.want {
			t.Fatalf("%+v %s %v", tt, got, err)
		}
	}
	for _, json := range []bool{false, true} {
		got, err := SelectJSONFormat("yaml", json)
		if got != "" || err == nil {
			t.Fatalf("invalid %s %v", got, err)
		}
		if json && !strings.Contains(err.Error(), "--json cannot be combined with --format=yaml") {
			t.Fatal(err)
		}
	}
}

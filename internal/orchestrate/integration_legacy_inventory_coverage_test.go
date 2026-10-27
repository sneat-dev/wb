package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type inventoryReadOnlyGitHub struct {
	PromoteGitHub
	body  string
	err   error
	reads int
}

func (g *inventoryReadOnlyGitHub) OpenPullRequest(context.Context, string, string, string) (int, string, error) {
	g.reads++
	return 7, g.body, g.err
}

func TestLegacyStoredInventoryInputsRetainImmutableBinding(t *testing.T) {
	t.Parallel()
	binding, _ := legacyTestBinding(legacyTestRecord())
	for _, kind := range []string{"new", "same", "conflict", "missing separator", "invalid URL", "invalid stored", "read failure"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			gh := &inventoryReadOnlyGitHub{}
			o := PromoteOptions{Repository: "acme/app", Integration: "integration", To: "main", GitHub: gh, LegacyRecords: []string{binding.URL + "=" + binding.Digest}}
			wantErr := ""
			switch kind {
			case "same", "conflict":
				gh.body = legacyInventoryPrefix + legacyJSON([]LegacyBinding{binding}) + " -->"
				if kind == "conflict" {
					o.LegacyRecords = []string{binding.URL + "=" + legacyHash([]byte("changed"))}
					wantErr = "digest conflict"
				}
			case "missing separator":
				o.LegacyRecords = []string{"invalid"}
				wantErr = "legacy record must"
			case "invalid URL":
				o.LegacyRecords = []string{"file:///bad=" + binding.Digest}
				wantErr = "legacy"
			case "invalid stored":
				gh.body = legacyInventoryPrefix + "bad -->"
				wantErr = "syntax"
			case "read failure":
				gh.err = errors.New("inventory read unavailable")
				wantErr = gh.err.Error()
			}
			got, err := legacyRecordBindings(context.Background(), o)
			if gh.reads != 1 {
				t.Fatalf("inventory reads=%d", gh.reads)
			}
			if wantErr != "" {
				var syntax *json.SyntaxError
				if wantErr == "syntax" && errors.As(err, &syntax) {
					return
				}
				if err == nil || !strings.Contains(err.Error(), wantErr) {
					t.Fatalf("%s: got %v", kind, err)
				}
				if len(got) != 0 {
					t.Fatalf("partial bindings on failure: %+v", got)
				}
				return
			}
			if err != nil || len(got) != 1 || got[0] != binding {
				t.Fatalf("%s: %+v %v", kind, got, err)
			}
		})
	}
}

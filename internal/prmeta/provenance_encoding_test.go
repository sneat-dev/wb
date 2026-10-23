package prmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAppendRoundTripsEscapedCoordinationIdentity(t *testing.T) {
	t.Parallel()
	identity := Provenance{Effort: "  effort\twith\"quote  ", Stream: "  stream\\name  "}
	got := Append("Summary.\n\n", identity)
	start := strings.Index(got, marker)
	end := strings.Index(got[start:], " -->") + start
	var decoded Provenance
	if err := json.Unmarshal([]byte(got[start+len(marker):end]), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Effort != strings.TrimSpace(identity.Effort) || decoded.Stream != strings.TrimSpace(identity.Stream) {
		t.Fatalf("identity = %+v", decoded)
	}
	if appended := Append("Summary.", Provenance{Effort: " \t "}); appended != "Summary." {
		t.Fatalf("blank effort altered body: %q", appended)
	}
}

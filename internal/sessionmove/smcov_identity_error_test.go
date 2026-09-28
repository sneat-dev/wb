package sessionmove

import (
	"errors"
	"strings"
	"testing"
)

func TestSmCovIdentityGenerationPropagatesEntropyFailure(t *testing.T) {
	t.Parallel()
	fail := func([]byte) (int, error) { return 0, errors.New("entropy unavailable") }
	if id, err := newHandoffID(fail); err == nil || id != "" || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("newHandoffID failure = id %q, err %v", id, err)
	}
	if id, err := newMessageID(fail); err == nil || id != "" || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("newMessageID failure = id %q, err %v", id, err)
	}
}

func TestSmCovWorkLogReferenceRejectsDotPathSegments(t *testing.T) {
	t.Parallel()
	claim := strings.Repeat("a", 64)
	for _, value := range []string{
		"worklog:./run/" + claim,
		"worklog:../run/" + claim,
		"worklog:effort/./" + claim,
		"worklog:effort/../" + claim,
	} {
		if _, err := ParseWorkLogReference(value); err == nil {
			t.Fatalf("ParseWorkLogReference(%q) succeeded", value)
		}
	}
}

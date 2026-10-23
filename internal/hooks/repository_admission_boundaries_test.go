package hooks

import (
	"errors"
	"io"
	"strings"
	"testing"
)

type forbiddenPushReader struct{ t *testing.T }

func (reader forbiddenPushReader) Read([]byte) (int, error) {
	reader.t.Fatal("interactive invocation drained stdin")
	return 0, io.EOF
}

func TestInteractivePushClassificationDoesNotReadInputOrInspectRepository(t *testing.T) {
	t.Parallel()
	input := forbiddenPushReader{t: t}
	classification, err := classifyPendingPushWithTerminal(input, "unavailable repository", func(reader any) bool { return reader == input })
	if err != nil || classification.Tier != TierLint || !strings.Contains(classification.Reason, "interactive invocation") {
		t.Fatalf("classification=%+v error=%v", classification, err)
	}
}

func TestRepositoryRootRetainsAbsoluteResolutionFailure(t *testing.T) {
	t.Parallel()
	refused := errors.New("working directory unavailable")
	seen := ""
	root, err := repositoryRootWithAbsolute("  ", func(path string) (string, error) { seen = path; return "", refused })
	if root != "" || !errors.Is(err, refused) || seen != "." {
		t.Fatalf("root=%q error=%v resolver input=%q", root, err, seen)
	}
}

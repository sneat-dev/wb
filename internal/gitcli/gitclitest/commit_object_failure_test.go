package gitclitest

import (
	"context"
	"errors"
	"testing"
)

func TestCommitObjectFailureTargetsOneScriptedCall(t *testing.T) {
	t.Parallel()
	fake := &Fake{CommitObjectExistsByCase: map[string]BoolResult{key("repo", "sha"): {Value: true}}}
	failure := errors.New("fetch unavailable")
	fake.FailCall(1, failure)
	if exists, err := fake.CommitObjectExists(context.Background(), "repo", "sha"); exists || !errors.Is(err, failure) {
		t.Fatalf("injected call = %v, %v", exists, err)
	}
	if exists, err := fake.CommitObjectExists(context.Background(), "repo", "sha"); !exists || err != nil {
		t.Fatalf("following call = %v, %v", exists, err)
	}
	if count := fake.CallCount(); count != 2 {
		t.Fatalf("answered calls = %d", count)
	}
}

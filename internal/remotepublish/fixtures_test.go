package remotepublish

import (
	"os"
	"path/filepath"
	"testing"
)

type testCodedError struct {
	code    int
	message string
}

func (e *testCodedError) Error() string { return e.message }
func testExitError(code int, message string) error {
	return &testCodedError{code: code, message: message}
}

type testLearnedLogin struct{ value string }

func (l *testLearnedLogin) learn(s string) { l.value = s }
func (l *testLearnedLogin) known() string  { return l.value }
func cockpitConfigFile(t *testing.T, content string) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() string { return path }
}

func fakeClone(t *testing.T, root, name string) string {
	t.Helper()
	clone := filepath.Join(root, "acme", name)
	if err := os.MkdirAll(filepath.Join(clone, ".git", "refs", "heads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return clone
}
func moveRef(t *testing.T, clone, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(clone, ".git", "refs", "heads", "main"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

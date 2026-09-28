package layout

import (
	"strings"
	"testing"
)

func TestUnknownIncludeTaskErrorNamesTheTask(t *testing.T) {
	t.Parallel()
	err := &UnknownIncludeTaskError{Task: "cov-u-11"}
	if !strings.Contains(err.Error(), `"cov-u-11"`) {
		t.Fatalf("UnknownIncludeTaskError.Error() = %q, want it to name the task", err.Error())
	}
	if !strings.Contains(err.Error(), "--include-task") {
		t.Fatalf("UnknownIncludeTaskError.Error() = %q, want it to name the flag", err.Error())
	}
}

func TestUndoIncludeFlagsErrorExplainsWhy(t *testing.T) {
	t.Parallel()
	err := &UndoIncludeFlagsError{}
	if !strings.Contains(err.Error(), "--undo") || !strings.Contains(err.Error(), "--include-task") {
		t.Fatalf("UndoIncludeFlagsError.Error() = %q, want it to name --undo and --include-task", err.Error())
	}
}

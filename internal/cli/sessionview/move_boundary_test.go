package sessionview

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionrun"
)

type moveBoundaryWriter struct{ err error }

func (w moveBoundaryWriter) Write([]byte) (int, error) { return 0, w.err }

func TestMoveResumeRenderingUsesReceiptAndPropagatesWriterFailure(t *testing.T) {
	t.Parallel()
	result := sessionrun.MoveResult{Resume: true, Courier: sessionmove.CourierSSH,
		Request: sessionmove.Request{HandoffID: "handoff-exact"},
		Receipt: &sessionmove.Receipt{SuccessorWBSessionID: "successor-exact", TmuxName: "tmux-exact"}}
	var out bytes.Buffer
	if err := Move(&out, "text", result); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "completed handoff handoff-exact to successor successor-exact via ssh in tmux tmux-exact; predecessor custody is sealed\n"; got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
	want := errors.New("current writer refused receipt")
	if err := Move(moveBoundaryWriter{want}, "text", result); !errors.Is(err, want) {
		t.Fatalf("writer error=%v", err)
	}
	out.Reset()
	if err := Move(&out, "json", result); err != nil || strings.Contains(out.String(), `"Resume"`) || strings.Contains(out.String(), `"resume"`) {
		t.Fatalf("internal resume selector leaked: %q, error=%v", out.String(), err)
	}
}

package cmdsession

import (
	"bytes"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"strings"
	"testing"
)

func TestWriteSessionResumeOutputJSONEncodesFields(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	err := writeSessionResumeOutput(command, "json", sessionrun.ResumeResult{ParkedSessionID: "p1", Status: "resumed"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), `"parked_session_id": "p1"`) {
		t.Fatalf("want json output carrying parked_session_id, got %q", out.String())
	}
}
func TestWriteSessionResumeOutputTextReportsReplayed(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	var out bytes.Buffer
	command.SetOut(&out)
	err := writeSessionResumeOutput(command, "text", sessionrun.ResumeResult{
		ParkedSessionID: "p1", SuccessorWBSessionID: "s2", TargetMachine: "vm-2", MemberCount: 3, Replay: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "replayed parked session p1 as successor s2 on vm-2 with 3 worktrees") {
		t.Fatalf("want a replayed text summary, got %q", out.String())
	}
}
func TestReadParkContextReadsStdinOnDash(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("continuation text"))
	got, err := readParkContext(command, "-")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "continuation text" {
		t.Fatalf("want stdin contents returned verbatim, got %q", got)
	}
}
func TestReadSessionReceiveParkEnvelopeRejectsBlankInput(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{}
	command.SetIn(strings.NewReader("   "))
	_, err := readSessionReceiveParkEnvelope(command)
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("want empty-envelope refusal, got %v", err)
	}
}

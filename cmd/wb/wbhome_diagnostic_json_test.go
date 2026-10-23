package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestMachineReadableOutputRecognisesJSONRequests keeps the retired-WB_HOME
// warning out of any invocation a program is reading: --format json and the
// --json shortcut both count, other formats and commands without either do not.
func TestMachineReadableOutputRecognisesJSONRequests(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"format json", []string{"--format", "json"}, true},
		{"format JSON with padding", []string{"--format", " JSON "}, true},
		{"json shortcut", []string{"--json"}, true},
		{"format text", []string{"--format", "text"}, false},
		{"json shortcut off", []string{"--json=false"}, false},
		{"neither flag given", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := &cobra.Command{Use: "probe"}
			cmd.Flags().String("format", "text", "")
			cmd.Flags().Bool("json", false, "")
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := machineReadableOutput(cmd); got != tc.want {
				t.Fatalf("machineReadableOutput(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
	bare := &cobra.Command{Use: "bare"}
	if machineReadableOutput(bare) {
		t.Fatal("a command with neither flag reported machine-readable output")
	}
}

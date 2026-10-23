package githubobserver

import (
	"errors"
	"testing"
)

func TestCommandDiagnosticKeepsStderrFirstAndTrims(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		response CommandResponse
		want     string
	}{
		"stderr wins":              {CommandResponse{Stderr: []byte(" stderr detail "), Stdout: []byte("stdout")}, "stderr detail"},
		"stdout":                   {CommandResponse{Stdout: []byte(" stdout detail ")}, "stdout detail"},
		"error":                    {CommandResponse{Err: errors.New("command failed")}, "command failed"},
		"empty":                    {CommandResponse{}, ""},
		"blank stderr uses stdout": {CommandResponse{Stderr: []byte(" \n "), Stdout: []byte(" next ")}, "next"},
		"stderr suppresses error":  {CommandResponse{Stderr: []byte(" first "), Err: errors.New("later")}, "first"},
		"stdout suppresses error":  {CommandResponse{Stdout: []byte(" first "), Err: errors.New("later")}, "first"},
		"blank streams use error":  {CommandResponse{Stderr: []byte(" "), Stdout: []byte(" \n"), Err: errors.New("last")}, "last"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := CommandDiagnostic(test.response); got != test.want {
				t.Fatalf("CommandDiagnostic=%q,want %q", got, test.want)
			}
		})
	}
}

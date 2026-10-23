package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestVersionOutputFailuresAreFindings(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"version", "--json"}, {"version"}, {"--version"}, {"-v"}} {
		t.Run(args[0]+":"+args[len(args)-1], func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(args, failingWriter{err: io.ErrClosedPipe}, &stderr); code != exitFindings {
				t.Fatalf("run(%q) = %d, want %d; stderr=%s", args, code, exitFindings, stderr.String())
			}
		})
	}
}

func TestVersionWiringMatchesBuildSnapshot(t *testing.T) {
	t.Parallel()
	info := collectVersion()
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitOK || stdout.String() != info.Version+"\n" {
			t.Fatalf("%q code=%d stdout=%q stderr=%s", args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"version", "--json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var got versionInfo
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	info.Version = info.JSONVersion
	info.JSONVersion = ""
	if got != info {
		t.Fatalf("JSON=%+v want=%+v", got, info)
	}
}

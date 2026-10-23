//go:build windows

package filewrite

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsImmutablePublicationRefusesWithoutReplacingWinner(t *testing.T) {
	t.Parallel()
	for _, injected := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsupported", true: "injected failure"}[injected], func(t *testing.T) {
			t.Parallel()
			directory := openTestDir(t)
			winner := filepath.Join(directory.Name(), "winner")
			temporary := filepath.Join(directory.Name(), "temporary")
			if err := os.WriteFile(winner, []byte("winner"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(temporary, []byte("loser"), 0o600); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("publication refused")
			var inj *Injector
			if injected {
				inj = &Injector{Step: StepRenameNoReplace, Err: fault}
			}
			published, err := publishImmutableTemporaryAt(directory, "temporary", "winner", []byte("loser"), false, inj)
			if published || err == nil {
				t.Fatalf("unsupported publication = %t, %v", published, err)
			}
			if injected && !errors.Is(err, fault) {
				t.Fatalf("publication error = %v, want injected failure", err)
			}
			if !injected && !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("publication error = %v, want unsupported", err)
			}
			for path, want := range map[string]string{winner: "winner", temporary: "loser"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("read %s = %q, %v, want %q", path, got, err, want)
				}
			}
		})
	}
}

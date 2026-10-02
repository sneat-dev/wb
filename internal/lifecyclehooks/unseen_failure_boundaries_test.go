package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnseenWarningFailuresPreserveNativeCauses(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"state stat", "read directory", "read file", "quarantine", "remove"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			dispatcher, _ := hkCovEnv(t)
			if err := dispatcher.ensureState(); err != nil {
				t.Fatal(err)
			}
			if phase == "state stat" {
				dispatcher.StateDir = filepath.Join(t.TempDir(), "invalid\x00state")
			}
			path := filepath.Join(dispatcher.unseenDir(), "receipt.json")
			if phase != "state stat" && phase != "read directory" {
				raw := hkCovMustJSON(t, hkCovReceipt("receipt"))
				if phase == "quarantine" {
					raw = "{broken"
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			readDir := os.ReadDir
			if phase == "read directory" {
				readDir = func(path string) ([]os.DirEntry, error) {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					return os.ReadDir(path)
				}
			}
			read := os.ReadFile
			if phase == "read file" || phase == "remove" {
				read = func(path string) ([]byte, error) {
					raw, err := os.ReadFile(path)
					if err != nil {
						return nil, err
					}
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if phase == "read file" {
						return os.ReadFile(path)
					}
					return raw, nil
				}
			}
			if phase == "quarantine" {
				dispatcher.Now = func() time.Time {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					return time.Now()
				}
			}
			warnings, err := dispatcher.claimUnseenWarningsWithRead(1, readDir, read)
			if err == nil {
				t.Fatalf("failure accepted: %v", warnings)
			}
			if phase != "state stat" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native cause=%v", err)
			}
			if phase == "remove" && len(warnings) != 1 {
				t.Fatalf("warning already observed was lost: %v", warnings)
			}
		})
	}
}

package lifecyclehooks

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigLoaderPreservesOwnedOpenAndEncodingFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"vanished", "open", "stat", "identity", "marshal"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			dispatcher, _ := hkCovEnv(t)
			path := dispatcher.ConfigPath
			failure := errors.New("configuration encoding failed")
			var held *os.File
			open := func(name string) (*os.File, error) {
				if name != path {
					t.Fatalf("config path=%q", name)
				}
				var err error
				switch phase {
				case "vanished":
					if err := os.Remove(name); err != nil {
						t.Fatal(err)
					}
					return os.Open(name)
				case "open":
					return os.Open(filepath.Join(path, "not-a-directory"))
				case "identity":
					held, err = os.Open(hkCovWriteFile(t, filepath.Join(t.TempDir(), "other.yaml"), hkCovValidConfigYAML("/bin/true"), 0600))
				default:
					held, err = os.Open(name)
				}
				if err == nil && phase == "stat" {
					if err := held.Close(); err != nil {
						t.Fatal(err)
					}
				}
				return held, err
			}
			marshal := yaml.Marshal
			if phase == "marshal" {
				marshal = func(any) ([]byte, error) { return nil, failure }
			}
			_, found, err := loadWithIO(path, open, marshal)
			if phase == "vanished" {
				if found || err != nil {
					t.Fatalf("vanished config=%t %v", found, err)
				}
				return
			}
			if found || err == nil {
				t.Fatalf("failed config accepted: %t %v", found, err)
			}
			switch phase {
			case "open":
				if !strings.Contains(err.Error(), "read lifecycle hooks config") {
					t.Fatalf("open error=%v", err)
				}
			case "stat":
				if !errors.Is(err, os.ErrClosed) {
					t.Fatalf("stat cause=%v", err)
				}
			case "identity":
				if !strings.Contains(err.Error(), "changed while opening") {
					t.Fatalf("identity error=%v", err)
				}
			case "marshal":
				if !errors.Is(err, failure) {
					t.Fatalf("encoding cause=%v", err)
				}
			}
			if held != nil {
				if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("config descriptor left open: %v", err)
				}
			}
		})
	}
}

package daemonruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBridgeFileStagesRetainNativeSecurityAndEffectErrors(t *testing.T) {
	t.Parallel()
	failure := errors.New("private filesystem stage failed")
	t.Run("key read after real inspection", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if _, err := daemonFileBridgeKey(root, true); err != nil {
			t.Fatal(err)
		}
		files := nativeBridgeFileStages()
		files.read = func(string) ([]byte, error) { return nil, failure }
		_, err := daemonFileBridgeKeyInjectedWithStages(root, false, nil, files)
		if !errors.Is(err, failure) || !strings.Contains(err.Error(), "read daemon file bridge key") {
			t.Fatalf("error = %v", err)
		}
	})
	for _, stage := range []string{"path", "descriptor"} {
		t.Run("key protect "+stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			files := nativeBridgeFileStages()
			if stage == "path" {
				files.protect = func(string) error { return failure }
			} else {
				files.protectFile = func(*os.File) error { return failure }
			}
			_, err := daemonFileBridgeKeyInjectedWithStages(root, true, nil, files)
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			path, err := daemonFileBridgeKeyPath(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("failed key left behind: %v", err)
			}
		})
	}
	for _, parent := range []bool{false, true} {
		for _, stage := range []string{"reinspect", "protect"} {
			t.Run(strings.Join([]string{"directory", map[bool]string{true: "parent", false: "leaf"}[parent], stage}, "/"), func(t *testing.T) {
				t.Parallel()
				path := filepath.Join(t.TempDir(), "private")
				files := nativeBridgeFileStages()
				if stage == "protect" {
					files.protect = func(string) error { return failure }
				} else {
					calls := 0
					files.lstat = func(path string) (os.FileInfo, error) {
						calls++
						if calls == 2 {
							return nil, failure
						}
						return os.Lstat(path)
					}
				}
				var err error
				if parent {
					err = secureBridgeParentDirectoryWithStages(path, true, files)
				} else {
					err = secureBridgeDirectoryWithStages(path, files)
				}
				if err == nil {
					t.Fatal("filesystem failure succeeded")
				}
				if stage == "protect" && !errors.Is(err, failure) {
					t.Fatalf("error = %v", err)
				}
			})
		}
	}
	t.Run("envelope read", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := writeDaemonFileEnvelope(dir, "request", daemonFileEnvelope{ID: "request"}); err != nil {
			t.Fatal(err)
		}
		files := nativeBridgeFileStages()
		files.read = func(string) ([]byte, error) { return nil, failure }
		if _, err := readDaemonFileEnvelopeWithStages(filepath.Join(dir, "request.json"), files); !errors.Is(err, failure) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("envelope protect cleans temporary", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		files := nativeBridgeFileStages()
		files.protect = func(string) error { return failure }
		if err := writeDaemonFileEnvelopeInjectedWithStages(dir, "request", daemonFileEnvelope{}, nil, files); !errors.Is(err, failure) {
			t.Fatalf("error = %v", err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("temporary files = %v, %v", entries, err)
		}
	})
	for _, directory := range []string{"responses", "requests", "quarantine"} {
		t.Run("cleanup inspection/"+directory, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			dirs := map[string]string{}
			for _, name := range []string{"responses", "requests", "inflight", "quarantine"} {
				dirs[name] = filepath.Join(root, name)
				if err := os.Mkdir(dirs[name], 0700); err != nil {
					t.Fatal(err)
				}
			}
			candidate := filepath.Join(dirs[directory], "candidate.json")
			if err := os.WriteFile(candidate, []byte("receipt"), 0600); err != nil {
				t.Fatal(err)
			}
			files := nativeBridgeFileStages()
			files.info = func(os.DirEntry) (os.FileInfo, error) { return nil, failure }
			var err error
			if directory == "quarantine" {
				err = cleanupDaemonFileQuarantineWithStages(dirs[directory], time.Now(), files)
			} else {
				server := FileBridgeServer{requests: dirs["requests"], responses: dirs["responses"], inflight: dirs["inflight"]}
				err = server.cleanupStaleWithStages(time.Now(), files)
			}
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if _, err := os.Stat(candidate); err != nil {
				t.Fatalf("inspection failure deleted evidence: %v", err)
			}
		})
	}
}

func TestBridgeResolvedDirectoryAndQuarantineStagesPreserveNativeEvidence(t *testing.T) {
	t.Parallel()
	failure := errors.New("resolved directory effect refused")
	for _, at := range []int{1, 2} {
		t.Run(fmt.Sprint("resolution-", at), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			stages := nativeBridgeFileStages()
			native := stages.runtimeDir
			calls := 0
			stages.runtimeDir = func(actual string) (string, error) {
				calls++
				if calls == at {
					return "", failure
				}
				return native(actual)
			}
			if _, _, err := prepareDaemonFileBridgeWithStages(root, stages); !errors.Is(err, failure) {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if calls != at {
				t.Fatalf("later resolver executed after failure: %d", calls)
			}
		})
	}
	t.Run("real directory replaced before final inspection", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "private")
		stages := nativeBridgeFileStages()
		calls := 0
		stages.lstat = func(path string) (os.FileInfo, error) {
			calls++
			if calls == 2 {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("unsafe replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			return os.Lstat(path)
		}
		if err := secureBridgeDirectoryWithStages(path, stages); err == nil || !strings.Contains(err.Error(), "not a real directory") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("post-quarantine metadata refusal preserves moved receipt", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		requests, responses, err := prepareDaemonFileBridge(root)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(requests, "bad.json")
		if err := os.WriteFile(path, []byte("receipt"), 0600); err != nil {
			t.Fatal(err)
		}
		server := FileBridgeServer{requests: requests, responses: responses, errors: make(chan error, 1)}
		stages := nativeBridgeFileStages()
		stages.info = func(os.DirEntry) (os.FileInfo, error) { return nil, failure }
		if err := server.quarantineWithStages("bad", path, "malformed", stages); !errors.Is(err, failure) {
			t.Fatalf("error=%v", err)
		}
		select {
		case err := <-server.errors:
			if !errors.Is(err, failure) {
				t.Fatalf("reported=%v", err)
			}
		default:
			t.Fatal("cleanup refusal was not reported")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("original was not moved: %v", err)
		}
		files, err := os.ReadDir(filepath.Join(filepath.Dir(requests), "quarantine"))
		if err != nil || len(files) != 1 {
			t.Fatalf("quarantine=%v,%v", files, err)
		}
	})
}

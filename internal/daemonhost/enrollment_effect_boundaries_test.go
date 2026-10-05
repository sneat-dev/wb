package daemonhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

func TestEnrollmentCredentialEffectRefusalsPreserveRealAuthStores(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"remove", "write"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			if stage == "write" && (runtime.GOOS == "windows" || os.Geteuid() == 0) {
				t.Skip("this platform/user does not enforce the Unix directory write refusal")
			}
			config := memoryHubConfig(t)
			mount, err := mountHub(t.Context(), config, "127.0.0.1:8796", narrate.Writer{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = mount.Close() })
			_, resolver, _ := hub.NewMachineStores(mount.PeerAdmin.Backend)
			pepper, err := hubPepper(hubStoreDirectoryFromConfig(t, config))
			if err != nil {
				t.Fatal(err)
			}
			credentialDirectory := filepath.Join(filepath.Dir(config), "credentials")
			tokenFile := filepath.Join(credentialDirectory, "hub-local-"+mount.Machine+".token")
			if err := os.Remove(tokenFile); err != nil {
				t.Fatal(err)
			}
			if stage == "remove" {
				if err := os.Mkdir(tokenFile, 0700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(tokenFile, "child"), "private")
			} else {
				if err := os.Chmod(credentialDirectory, 0500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(credentialDirectory, 0700) })
			}
			err = ensureLocalEnrollment(t.Context(), mount.Enrollment, resolver, mount.viewer, config, mount.Machine, pepper, "127.0.0.1:8796")
			if err == nil {
				t.Fatal("private credential effect refusal was swallowed")
			}
			if stage == "remove" && !strings.Contains(err.Error(), "replace local hub credential") {
				t.Fatalf("replacement refusal=%v", err)
			}
			if stage == "write" && !errors.Is(err, os.ErrPermission) {
				t.Fatalf("write refusal=%v", err)
			}
		})
	}
}

func TestEnrollmentCurrentRejectsMissingAndMalformedActualCredential(t *testing.T) {
	t.Parallel()
	config := memoryHubConfig(t)
	mount, err := mountHub(t.Context(), config, "127.0.0.1:8796", narrate.Writer{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	_, resolver, _ := hub.NewMachineStores(mount.PeerAdmin.Backend)
	pepper, err := hubPepper(hubStoreDirectoryFromConfig(t, config))
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(filepath.Dir(config), "credentials", "hub-local-"+mount.Machine+".token")
	if err := os.Remove(tokenFile); err != nil {
		t.Fatal(err)
	}
	if localEnrollmentIsCurrent(context.Background(), resolver, config, "http://127.0.0.1:8796", mount.Machine, tokenFile, pepper) {
		t.Fatal("absent actual credential was current")
	}
	write(t, tokenFile, "  \n")
	if localEnrollmentIsCurrent(context.Background(), resolver, config, "http://127.0.0.1:8796", mount.Machine, tokenFile, pepper) {
		t.Fatal("empty actual credential was current")
	}
}

func TestHubMountReturnsActualPepperAndStoreOpenRefusals(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "blocked")
	write(t, blocked, "file")
	config := hubTestConfig(t, "hub:\n  store:\n    engine: memory\n    path: "+blocked+"\n")
	if mount, err := mountHub(t.Context(), config, "127.0.0.1:8796", narrate.Writer{}, nil); mount != nil || err == nil {
		t.Fatalf("pepper mount=%v,error=%v", mount, err)
	}
	t.Run("schema directory mode", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("this platform/user does not enforce the Unix schema-directory write refusal")
		}
		directory := t.TempDir()
		if err := os.Chmod(directory, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0700) })
		config := hubTestConfig(t, "hub:\n  store:\n    engine: ingitdb\n    path: "+directory+"\n")
		if mount, err := mountHub(t.Context(), config, "127.0.0.1:8796", narrate.Writer{}, nil); mount != nil || err == nil {
			t.Fatalf("store mount=%v,error=%v", mount, err)
		}
	})
}

package daemonhost

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
)

func TestHubBuilderPreservesNameObservationAndActualCredentialRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"hostname observation", "credential parent"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			config := memoryHubConfig(t)
			cfg, found, err := hubconfig.Load(config)
			if err != nil || !found {
				t.Fatalf("config=%+v found=%v err=%v", cfg, found, err)
			}
			store, closer, err := hubstore.Open(t.Context(), cfg.Store)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = closer.Close() })
			hostname := os.Hostname
			want := "replace local hub credential"
			if stage == "hostname observation" {
				hostname = func() (string, error) { return "", errors.New("simulated hostname observation") }
				want = "hub machine name could not be resolved; set remote.machine in wb.yaml"
			} else {
				write(t, filepath.Join(filepath.Dir(config), "credentials"), "private regular file")
			}
			mount, err := buildHubMount(t.Context(), cfg, store, config, "127.0.0.1:8796", narrate.Writer{Out: io.Discard}, nil, hostname)
			if mount != nil || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("mount=%v refusal=%v want %q", mount, err, want)
			}
			if stage == "hostname observation" {
				if _, err = os.Stat(filepath.Join(hubStateDirectory(cfg), "pepper")); !os.IsNotExist(err) {
					t.Fatalf("later pepper effect occurred: %v", err)
				}
			} else {
				raw, err := os.ReadFile(filepath.Join(filepath.Dir(config), "credentials"))
				if err != nil || string(raw) != "private regular file" {
					t.Fatalf("blocked parent changed=%q %v", raw, err)
				}
			}
		})
	}
}

func TestServeReturnsGenericListenErrorBeforeAnyStateWrite(t *testing.T) {
	t.Parallel()
	h, r, store := nativeServeFixture(t)
	sentinel := errors.New("simulated generic listener refusal")
	h.deps.Listen = func(network, address string) (net.Listener, error) {
		if network != "tcp" || address != r.Listen {
			t.Errorf("listen=%q %q", network, address)
		}
		return nil, sentinel
	}
	err := h.Serve(context.Background(), r, io.Discard, io.Discard)
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "listen for WB daemon on "+r.Listen) {
		t.Fatalf("Serve=%v", err)
	}
	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("state written after listener refusal: %v %v", found, err)
	}
}

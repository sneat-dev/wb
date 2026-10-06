package cockpitrun

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cockpit"
)

func TestLocalMintValidatesOwnerResponseAndPreservesContext(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("owner unavailable")
	for name, test := range map[string]struct {
		code, key, path string
		err             error
		want            string
	}{
		"successful admission": {"code", "key", cockpit.LoginPath, nil, ""},
		"RPC refusal":          {"", "", "", sentinel, "owner unavailable"},
		"missing code":         {"", "key", cockpit.LoginPath, nil, "no code"},
		"legacy missing key":   {"code", "", cockpit.LoginPath, nil, "older wb"},
		"wrong path":           {"code", "key", "/elsewhere", nil, "/elsewhere"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			deps := daemonTestDependencies(t, root)
			client := &http.Client{}
			deps.LocalClient = func(gotRoot, token string) (*http.Client, error) {
				if gotRoot != root || token == "" {
					t.Fatalf("owner binding %q %q", gotRoot, token)
				}
				return client, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			service := NewLocalService(deps, func(got context.Context, gotClient *http.Client) (cockpit.LoginCode, string, error) {
				calls++
				if got != ctx || gotClient != client {
					t.Fatal("lost owner client/context")
				}
				return cockpit.LoginCode{Code: test.code, Key: test.key}, test.path, test.err
			})
			result, err := service.Local(ctx, LocalRequest{Root: root, Mint: true})
			if calls != 1 {
				t.Fatalf("login calls=%d", calls)
			}
			if test.want == "" {
				if err != nil || result.Code != test.code || result.Key != test.key || result.Path != test.path {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.want) || result != (LocalSession{}) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("lost RPC identity: %v", err)
			}
			if name == "legacy missing key" && !errors.Is(err, ErrNoSessionKey) {
				t.Fatalf("lost legacy identity: %v", err)
			}
		})
	}
}

func TestLocalMintPropagatesOwnerClientFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	deps := daemonTestDependencies(t, root)
	sentinel := errors.New("socket refused")
	deps.LocalClient = func(string, string) (*http.Client, error) { return nil, sentinel }
	result, err := NewLocalService(deps, func(context.Context, *http.Client) (cockpit.LoginCode, string, error) {
		t.Fatal("login attempted after client failure")
		return cockpit.LoginCode{}, "", nil
	}).Local(context.Background(), LocalRequest{Root: root, Mint: true})
	if !errors.Is(err, sentinel) || result != (LocalSession{}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

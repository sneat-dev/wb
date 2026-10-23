package peersrun

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func TestPeersUpstreamTrustChangeKeepsUnrelatedPeersUntouchedAndReportsJSON(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := wbconfig.SetPeersUpstream(config, "https://hub.example.test", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	service := New(Dependencies{ConfigPath: func() string { return config }}, JoinDependencies{})
	root := t.TempDir()
	var output bytes.Buffer
	_, handled, err := service.upstreamTrustChange(TrustRequest{Root: root, Peer: "another-peer", Action: Block})
	if err != nil || handled || output.Len() != 0 {
		t.Fatalf("unrelated peer handled=%t error=%v output=%q", handled, err, output.String())
	}
	for _, tc := range []struct{ verb, peer, want string }{
		{"Blocked", "upstream", "blocked"},
		{"Unblocked", "hub.example.test", "active"},
	} {
		output.Reset()
		action := Block
		if tc.verb == "Unblocked" {
			action = Unblock
		}
		result, handled, err := service.upstreamTrustChange(TrustRequest{Root: root, Peer: tc.peer, Action: action})
		if err == nil && handled {
			err = json.NewEncoder(&output).Encode(result.Response)
		}
		if err != nil || !handled {
			t.Fatalf("%s handled=%t error=%v", tc.verb, handled, err)
		}
		var response peers.TrustResponse
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.PeerID != "hub.example.test" || response.Trust != tc.want {
			t.Fatalf("%s response = %+v", tc.verb, response)
		}
	}
}

package daemonruntime

import (
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cockpit"
)

// The peer input matches the committed root peersRPCPrefix, while cockpit input uses its actual exported constant.
func TestCockpitLoginCodeRouteIsRefusedByTheFileBridge(t *testing.T) {
	t.Parallel()
	for _, procedure := range []string{cockpit.LoginCodeRPCPath, cockpit.LoginCodeRPCPath + "/", "/wb.peers.v1/invite"} {
		if _, _, err := daemonFilePrepareRequest(procedure, nil, "request-id"); err == nil || !strings.Contains(err.Error(), "refused an unknown RPC procedure") {
			t.Errorf("the file bridge prepared %s: %v", procedure, err)
		}
	}
}

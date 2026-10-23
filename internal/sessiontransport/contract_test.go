// This file is an external test package (sessiontransport_test), not the
// internal sessiontransport test files that sit beside it, specifically so
// it can import internal/sessiontransport/transporttest: that package
// imports internal/sessiontransport itself, which an *internal* test file
// (package sessiontransport) cannot do without an import cycle.
package sessiontransport_test

import (
	"testing"

	"github.com/sneat-dev/wb/internal/sessiontransport"
	"github.com/sneat-dev/wb/internal/sessiontransport/transporttest"
)

// TestNoneTransportSatisfiesContractSuite proves
// REQ:two-transport-implementations' shared-suite requirement against
// [sessiontransport.NoneTransport], the one implementation Task 2 ships.
func TestNoneTransportSatisfiesContractSuite(t *testing.T) {
	t.Parallel()
	transporttest.Suite(t, func() sessiontransport.Transport { return sessiontransport.NoneTransport{} })
}

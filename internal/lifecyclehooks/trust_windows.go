//go:build windows

package lifecyclehooks

import "os"

// Windows ACL ownership is not representable through os.FileInfo. Regular,
// executable, non-repository validation still applies; native ACL validation
// can be added without weakening Unix ownership enforcement.
func trustedExecutableOwner(os.FileInfo) error { return nil }

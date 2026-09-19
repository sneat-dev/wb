package herdr

import "os"

// osLookupEnv backs [OSLookupEnv]. It is split into its own tiny file so
// env_os_test.go can cover it in isolation from the rest of identity.go's
// pure logic, which is exercised entirely through injected fakes.
func osLookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

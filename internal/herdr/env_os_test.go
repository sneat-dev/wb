package herdr

import "testing"

func TestOsLookupEnv(t *testing.T) {
	t.Setenv("WB_HERDR_ENV_OS_PROBE", "value")

	value, ok := osLookupEnv("WB_HERDR_ENV_OS_PROBE")
	if !ok || value != "value" {
		t.Fatalf("osLookupEnv() = (%q, %v), want (\"value\", true)", value, ok)
	}
}

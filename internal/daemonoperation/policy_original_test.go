package daemonoperation

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // Process-wide environment changes in TestCwWtRequireDaemonRawExecutionPolicy; these rows share their parent environment and remain sequential.
func TestCwWtRequireDaemonRawExecutionPolicy(t *testing.T) {
	root := t.TempDir()
	if err := (Service{
		RawPolicy: func(string) (bool, string, error) { return true, "/tmp/policy", nil }}).RequireRawPolicy(root); err != nil {
		t.Fatalf("allowed policy: %v", err)
	}
	err := (Service{
		RawPolicy: func(string) (bool, string, error) { return false, "/tmp/policy", nil }}).RequireRawPolicy(root)
	if err == nil || !strings.Contains(err.Error(), "raw daemon execution is disabled") || !strings.Contains(err.Error(), "/tmp/policy") {
		t.Fatalf("denied policy = %v", err)
	}
	err = (Service{
		RawPolicy: func(string) (bool, string, error) { return false, "", errors.New("cwWt: policy unreadable") }}).RequireRawPolicy(root)
	if err == nil || !strings.Contains(err.Error(), "load daemon raw-execution policy") {
		t.Fatalf("policy load error = %v", err)
	}

	// A nil policy falls back to the default dependency's loader.
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	if err := (Service{}).RequireRawPolicy(root); err == nil || !strings.Contains(err.Error(), "raw daemon execution is disabled") {
		t.Fatalf("default policy = %v", err)
	}
}

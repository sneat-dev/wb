package defaultbranch

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRemoteInspectionFailureRemainsInReturnedReport(t *testing.T) {
	t.Parallel()
	service := New()
	service.deps.ConfigPath = func() string { return filepath.Join(t.TempDir(), "absent.yaml") }
	reads := 0
	service.deps.Read = func(_ context.Context, endpoint string) ([]byte, error) {
		reads++
		if endpoint != "repos/acme/app" {
			t.Fatal(endpoint)
		}
		return nil, errors.New("injected metadata failure")
	}
	report, err := service.Run(t.Context(), Request{Options: Options{Repositories: []string{"acme/app"}, Branch: "main", Parallel: 1}}, &bytes.Buffer{})
	if err != nil || reads != 1 || len(report.Repositories) != 1 || report.Repositories[0].Error != "injected metadata failure" {
		t.Fatal(report, reads, err)
	}
}

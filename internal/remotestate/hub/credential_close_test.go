package hub

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestCredentialRejectsCloseFailureWithoutReturningToken(t *testing.T) {
	t.Parallel()
	provider := &Provider{tokenFile: "credential-file"}
	failure := errors.New("close failed")
	token, err := provider.credentialOpened(func(path string) (io.ReadCloser, error) {
		if path != provider.tokenFile {
			t.Fatalf("credential path = %q", path)
		}
		return &dqCovFailingBody{closeErr: failure}, nil
	})
	if token != "" || err == nil || !strings.Contains(err.Error(), "close hub credential file") {
		t.Fatalf("credential = %q, %v", token, err)
	}
}

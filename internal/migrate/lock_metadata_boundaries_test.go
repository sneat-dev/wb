package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCampaignLockMetadataReportsNativeDescriptorFailures(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"truncate", "seek", "write"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			file, err := os.CreateTemp(t.TempDir(), "metadata")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			closeOwned := func() {
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var afterTruncate, afterSeek func()
			switch stage {
			case "truncate":
				closeOwned()
			case "seek":
				afterTruncate = closeOwned
			case "write":
				afterSeek = closeOwned
			}
			err = initializeCampaignLockMetadataWithHooks(file, "metadata", afterTruncate, afterSeek)
			if !errors.Is(err, os.ErrClosed) {
				t.Fatalf("error=%v", err)
			}
			raw, err := os.ReadFile(file.Name())
			if err != nil || len(raw) != 0 {
				t.Fatalf("incomplete metadata=%q error=%v", raw, err)
			}
		})
	}
}

func TestCampaignLockMetadataRejectsClosedReader(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metadata")
	if err := os.WriteFile(path, []byte("migration=record\npid=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if validCampaignLockMetadata(file, "record") {
		t.Fatal("closed descriptor attested campaign ownership")
	}
}

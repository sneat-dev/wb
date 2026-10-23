package locallink

import (
	"github.com/sneat-dev/wb/internal/streams"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkGuardsRefuseCorruptPhysicalStreamInventory(t *testing.T) {
	t.Parallel()
	store := streams.OpenAt(filepath.Join(t.TempDir(), "streams"))
	record := filepath.Join(store.Root, "broken", "stream.json")
	if err := os.MkdirAll(filepath.Dir(record), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, []byte("{malformed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	links, err := HasLiveLink(store, "/private/consumer")
	sources, sourceErr := HasLiveLinkSource(store, "/private/library")
	if err == nil || sourceErr == nil || len(links) != 0 || len(sources) != 0 || !strings.Contains(err.Error(), record) || !strings.Contains(sourceErr.Error(), record) {
		t.Fatalf("guard authorized unreadable inventory: %v %v", err, sourceErr)
	}
}

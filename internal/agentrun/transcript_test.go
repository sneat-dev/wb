package agentrun

import (
	"errors"
	"github.com/sneat-dev/wb/internal/agents"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranscriptReadsActualSummaryRawAndMissingPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "events")
	if err := os.WriteFile(path, []byte(`{"type":"item.completed","item":{"type":"command_execution","command":"cat a.txt"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reader := DefaultTranscriptReader()
	for _, raw := range []bool{false, true} {
		text, err := reader.Read(agents.Record{LogPath: path}, 0, raw)
		if err != nil || !strings.Contains(text, "cat a.txt") || !strings.HasPrefix(text, path+"\n") {
			t.Fatal(text, err)
		}
	}
	for _, path := range []string{" ", filepath.Join(dir, "missing"), dir} {
		text, err := reader.Read(agents.Record{LogPath: path}, 8, false)
		if err != nil || !strings.Contains(text, "no worker transcript") {
			t.Fatal(text, err)
		}
	}
}
func TestTranscriptOpenAndRawReadFailuresPreserveIdentityAndClose(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("transcript failed")
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	info = regularInfo{info}
	reader := TranscriptReader{Stat: func(string) (os.FileInfo, error) { return info, nil }, Open: func(string) (io.ReadCloser, error) { return nil, sentinel }}
	if _, err := reader.Read(agents.Record{LogPath: "log"}, 8, true); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	closed := false
	reader.Open = func(string) (io.ReadCloser, error) { return closingReader{errorReader{sentinel}, &closed}, nil }
	if _, err := reader.Read(agents.Record{LogPath: "log"}, 8, true); err != sentinel || !closed {
		t.Fatal(err, closed)
	}
}

type regularInfo struct{ os.FileInfo }

func (regularInfo) Mode() os.FileMode { return 0600 }

type closingReader struct {
	io.Reader
	closed *bool
}

func (r closingReader) Close() error { *r.closed = true; return nil }

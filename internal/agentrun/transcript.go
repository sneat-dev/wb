package agentrun

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/agents"
	"io"
	"os"
	"strings"
)

type TranscriptReader struct {
	Stat func(string) (os.FileInfo, error)
	Open func(string) (io.ReadCloser, error)
}

func DefaultTranscriptReader() TranscriptReader {
	return TranscriptReader{Stat: os.Stat, Open: func(path string) (io.ReadCloser, error) { return os.Open(path) }}
}
func (r TranscriptReader) Read(record agents.Record, tail int, raw bool) (string, error) {
	var builder strings.Builder
	builder.WriteString(record.LogPath + "\n")
	if !r.exists(record.LogPath) {
		builder.WriteString("(no worker transcript was captured)\n")
		return strings.TrimRight(builder.String(), "\n"), nil
	}
	file, err := r.Open(record.LogPath)
	if err != nil {
		return "", fmt.Errorf("open run log: %w", err)
	}
	defer func() { _ = file.Close() }()
	if raw {
		if _, err := io.Copy(&builder, file); err != nil {
			return "", err
		}
		return strings.TrimRight(builder.String(), "\n"), nil
	}
	if tail <= 0 {
		tail = 8
	}
	for _, action := range agents.RecentActions(file, tail) {
		builder.WriteString("  " + action + "\n")
	}
	return strings.TrimRight(builder.String(), "\n"), nil
}
func (r TranscriptReader) exists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := r.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

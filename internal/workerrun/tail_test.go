package workerrun

import (
	"bytes"
	"testing"
)

func TestCwCovWorkerTailBufferKeepsTheTailBounded(t *testing.T) {
	t.Parallel()
	var buffer workerTailBuffer
	chunk := bytes.Repeat([]byte("a"), 40<<10)
	if n, err := buffer.Write(chunk); err != nil || n != len(chunk) {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	marker := []byte("TAIL-MARKER")
	chunk2 := append(bytes.Repeat([]byte("b"), 40<<10), marker...)
	if _, err := buffer.Write(chunk2); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() > 64<<10 {
		t.Fatalf("buffer length = %d, want it bounded at 64KiB", buffer.Len())
	}
	if !bytes.HasSuffix(buffer.Bytes(), marker) {
		t.Fatalf("the retained window must be the tail, ending in the newest bytes")
	}
}

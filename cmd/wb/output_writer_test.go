package main

import "fmt"

// failAfterWriter exposes the precise write-failure boundary to stream tests.
type failAfterWriter struct{ allowedWrites, calls int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls > w.allowedWrites {
		return 0, fmt.Errorf("zz_rwi03: forced write failure on call %d", w.calls)
	}
	return len(p), nil
}

package main

import (
	"errors"
)

var errAtWrite = errors.New("write failed")

type failAtCallWriter struct {
	calls  int
	failAt int
}

func (w *failAtCallWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		return 0, errAtWrite
	}
	return len(p), nil
}

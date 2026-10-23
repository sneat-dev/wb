package main

// exitError reports a command that ran to completion but found problems worth
// a non-zero exit. The message must name what was found and where to look: an
// agent sees only that message and the exit code, so "failed" on its own tells
// it nothing it can act on.
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

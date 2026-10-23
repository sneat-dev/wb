package wbupdate

import (
	"bytes"
	"context"
	"os/exec"
)

// RunChild executes the verified provider identity with the requested capture mode.
func RunChild(ctx context.Context, request ChildRequest) (ChildResult, error) {
	child := exec.CommandContext(ctx, request.Path, request.Args...) //nolint:gosec // verified provider executable
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	if request.MergeOutput {
		child.Stderr = &stdout
	} else {
		child.Stderr = &stderr
	}
	err := child.Run()
	if request.MergeOutput {
		return ChildResult{Combined: stdout.Bytes()}, err
	}
	return ChildResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
}

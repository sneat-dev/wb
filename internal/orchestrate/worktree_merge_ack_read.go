package orchestrate

import (
	"encoding/json"
	"fmt"
	"os"
)

// readMergeAcknowledgement leaves result custody to the typed reader: some
// readers return the decoded partial document on failure, others return zero.
func readMergeAcknowledgement[T mergeAcknowledgementDocument](path, description string, document *T) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return decodeMergeAcknowledgement(contents, path, description, document)
}

// Decode is separate because supersession checks its receipt after reading the
// sidecar but before decoding it. That validation error must retain precedence.
func decodeMergeAcknowledgement[T mergeAcknowledgementDocument](contents []byte, path, description string, document *T) error {
	if err := json.Unmarshal(contents, document); err != nil {
		return fmt.Errorf("decode %s %s: %w", description, path, err)
	}
	return nil
}

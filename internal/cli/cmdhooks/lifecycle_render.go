package cmdhooks

import (
	"encoding/json"
	"io"
)

func writeLifecycleJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func lifecycleShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

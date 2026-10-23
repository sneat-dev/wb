package shared

import (
	"fmt"
	"strings"
)

// RequireOutputFormat checks an exact format name and explains the accepted
// spellings. It preserves the CLI's existing format diagnostic.
func RequireOutputFormat(value string, allowed ...string) error {
	for _, candidate := range allowed {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("unsupported format %q; use %s", value, strings.Join(allowed, " or "))
}

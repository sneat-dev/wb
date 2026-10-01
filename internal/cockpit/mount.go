package cockpit

import (
	"maps"
	"net/http"
)

func mergeMounts(base, extra map[string]http.Handler) map[string]http.Handler {
	merged := make(map[string]http.Handler, len(base)+len(extra))
	maps.Copy(merged, base)
	maps.Copy(merged, extra)
	return merged
}

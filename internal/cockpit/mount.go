package cockpit

import (
	"encoding/json"
	"maps"
	"net/http"

	"github.com/sneat-dev/wb/cockpit/web"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// APIHandler answers every Cockpit API route. No route exists yet, so each
// request gets a JSON 404; later tasks register the fleet and session routes
// here.
func APIHandler() http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(writer).Encode(map[string]string{"error": "not found"})
	})
}

// Options is what the daemon hands Cockpit at startup.
type Options struct {
	// CanonicalHost is the loopback name the listener has (see CanonicalHost).
	CanonicalHost string
	// Config is wb.yaml's cockpit: section, after defaults. Later tasks read
	// it; for now it is only carried.
	Config wbconfig.CockpitConfig
}

// Mounts returns Cockpit's two subtrees, each behind Guard with the options'
// canonical host, in the shape
// dashboard.Options.Mounts takes. They are mounted whether or not wb.yaml has
// a hub: section.
func Mounts(options Options) map[string]http.Handler {
	return mountsFor(options.CanonicalHost, web.Handler())
}

// MountsWith returns Cockpit's mounts added to others (the hub's, which is nil
// without a hub: section). The others are copied, never modified.
func MountsWith(others map[string]http.Handler, options Options) map[string]http.Handler {
	return mergeMounts(others, Mounts(options))
}

func mergeMounts(base, extra map[string]http.Handler) map[string]http.Handler {
	merged := make(map[string]http.Handler, len(base)+len(extra))
	maps.Copy(merged, base)
	maps.Copy(merged, extra)
	return merged
}

// mountsFor is Mounts over an injectable application handler.
func mountsFor(canonical string, app http.Handler) map[string]http.Handler {
	return map[string]http.Handler{
		PagePrefix: Guard(canonical, app),
		APIPrefix:  Guard(canonical, APIHandler()),
	}
}

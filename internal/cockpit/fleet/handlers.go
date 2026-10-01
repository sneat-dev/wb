package fleet

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/sneat-dev/wb/internal/cockpit"
)

// Routes the read model serves under cockpit.APIPrefix. The README route has
// no path parameter, because Cockpit's owner routes are exact paths: the
// repository's stable id travels in the "repository" query parameter.
const (
	FleetRoute  = "fleet"
	ReadmePath  = cockpit.APIPrefix + "readme"
	readmeQuery = "repository"
)

// Register adds the fleet metadata route and the owner-only README route to
// server. Call it before the server's mounts are taken.
func Register(server *cockpit.Server, snapshotter *Snapshotter) {
	server.HandleMetadata(FleetRoute, cockpit.CapabilityFleetRead, snapshotter.serveFleet)
	server.HandleOwner(http.MethodGet, ReadmePath, cockpit.CapabilityRepoContentRead, snapshotter.serveReadme)
}

// serveFleet answers the fleet read model from the last snapshot's marshalled
// bytes, with a strong ETag and If-None-Match support. It reads memory only: no
// collector runs on a request.
func (s *Snapshotter) serveFleet(writer http.ResponseWriter, request *http.Request, _ cockpit.Principal) {
	body, etag := s.Body()
	header := writer.Header()
	header.Set("ETag", etag)
	// no-cache, not no-store: a client may keep the document but must revalidate
	// it with the ETag on every use.
	header.Set("Cache-Control", "no-cache")
	for _, candidate := range strings.Split(request.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(candidate) == etag || strings.TrimSpace(candidate) == "*" {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = writer.Write(body)
}

// serveReadme answers the README of the repository named by id, read from Git's
// object store, never from the working tree and never by a path from the
// request.
func (s *Snapshotter) serveReadme(writer http.ResponseWriter, request *http.Request) {
	data, status, code := s.readme(request.Context(), request.URL.Query().Get(readmeQuery))
	if status != 0 {
		writeError(writer, status, code)
		return
	}
	header := writer.Header()
	header.Set("Content-Type", "text/markdown; charset=utf-8")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = writer.Write(data)
}

// writeError writes status with a JSON {"error": code} body.
func writeError(writer http.ResponseWriter, status int, code string) {
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": code})
}

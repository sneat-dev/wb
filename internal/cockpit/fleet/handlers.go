package fleet

import (
	"encoding/json"
	"net/http"

	"github.com/sneat-dev/wb/internal/cockpit"
)

// Routes the read model serves under cockpit.APIPrefix. The README route has
// no path parameter, because Cockpit's owner routes are exact paths: the
// repository's stable id travels in the "repository" query parameter.
const (
	FleetRoute    = "fleet"
	BranchesRoute = "branches"
	ReadmePath    = cockpit.APIPrefix + "readme"
	readmeQuery   = "repository"
)

// Register adds the fleet, branches and machine-metrics metadata routes and the owner-only README route to
// server. Call it before the server's mounts are taken.
func Register(server *cockpit.Server, snapshotter *Snapshotter) {
	server.HandleMetadata(FleetRoute, cockpit.CapabilityFleetRead, snapshotter.serveFleet)
	server.HandleMetadata(BranchesRoute, cockpit.CapabilityBranchRead, snapshotter.serveBranches)
	server.HandleMetadata(MetricsRoute, cockpit.CapabilityMachineRead, snapshotter.serveMetrics)
	server.HandleOwner(http.MethodGet, ReadmePath, cockpit.CapabilityRepoContentRead, snapshotter.serveReadme)
}

// serveFleet answers the fleet read model from the last snapshot's prepared
// bytes: gzip or identity with each encoding's strong ETag and If-None-Match
// support. It reads memory only: no collector runs on a request and no
// compressor either.
func (s *Snapshotter) serveFleet(writer http.ResponseWriter, request *http.Request, _ cockpit.Principal) {
	cockpit.ServePayload(writer, request, s.Payload())
}

// serveBranches answers the branches of the repository named by the
// "repository" query parameter, from the last scan (never from Git). An id
// this daemon does not know is a 404 with no data.
func (s *Snapshotter) serveBranches(writer http.ResponseWriter, request *http.Request, _ cockpit.Principal) {
	payload, found := s.Branches(request.URL.Query().Get(readmeQuery))
	if !found {
		writeError(writer, http.StatusNotFound, "unknown_repository")
		return
	}
	cockpit.ServePayload(writer, request, payload)
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

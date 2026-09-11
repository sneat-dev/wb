// Package web embeds the built bench dashboard so a self-hosted hub serves
// the same pages the hosted instance does, with no Node runtime and no
// configuration.
//
// dist/ is produced by `pnpm build` in this directory and is git-ignored
// except for dist/.gitkeep, which exists because go:embed refuses a directory
// that is not there. The Astro build copies public/.gitkeep back into dist/,
// so building never deletes the tracked placeholder. A wb built from a clean
// clone therefore compiles, and Handler serves a one-line page explaining how
// to build the real dashboard.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Dist holds every file the Astro build emitted, or only .gitkeep in a clone
// where `pnpm build` has never run.
//
//go:embed all:dist
var Dist embed.FS

// MountPath is where the dashboard is served. The Astro build hard-codes it
// as the site base, so every asset URL in the emitted HTML already starts
// here and the pages need no rewriting.
const MountPath = "/bench/"

// dashboardIndex is the page the journey in spec/features/self-hosted-bench
// opens; its presence is what tells Handler the dist is real rather than a
// placeholder.
const dashboardIndex = "dashboard/index.html"

const notBuiltPage = "The bench dashboard was not built into this wb binary. " +
	"Run `pnpm install && pnpm build` in hub/web, then rebuild wb.\n"

// Built reports whether a real dashboard is embedded.
func Built() bool {
	_, err := fs.Stat(distFS, dashboardIndex)
	return err == nil
}

// distFS is dist/ as a filesystem of its own, so a request path maps straight
// onto a file name. fs.Sub only fails on a malformed path and "dist" is a
// literal that go:embed has already resolved, so the error cannot happen; it
// is discarded here rather than turned into an unreachable branch.
var distFS, _ = fs.Sub(Dist, "dist")

// Handler serves the embedded dashboard under MountPath. It implements the
// Astro build's trailingSlash: 'always' + build.format: 'directory' contract:
// a request for a directory is answered with that directory's index.html, and
// a request that omits the trailing slash is redirected to the one that has
// it — the same shape a static host gives the hosted instance.
//
// Mount it on a mux at MountPath; the prefix is stripped here so the caller
// does not have to.
func Handler() http.Handler { return handlerFor(distFS, Built()) }

// handlerFor is Handler over an injectable tree, so the built and not-built
// pages are both testable in a checkout that has only one of them.
func handlerFor(files fs.FS, built bool) http.Handler {
	return http.StripPrefix(strings.TrimSuffix(MountPath, "/"), http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !built {
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(notBuiltPage))
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
		if name == "" || name == "." {
			// The site has no page at its base; the journey's entry point is
			// the dashboard, so send the operator there.
			http.Redirect(writer, request, MountPath+"dashboard/", http.StatusFound)
			return
		}
		if info, err := fs.Stat(files, name); err == nil && info.IsDir() {
			if !strings.HasSuffix(request.URL.Path, "/") {
				http.Redirect(writer, request, MountPath+name+"/", http.StatusFound)
				return
			}
			name = path.Join(name, "index.html")
		}
		content, err := fs.ReadFile(files, name)
		if err != nil {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", contentType(name))
		_, _ = writer.Write(content)
	}))
}

// contentType maps the handful of extensions the Astro build emits. The Go
// standard library's mime package consults the host's /etc/mime.types, which
// makes the served type vary by machine; a fixed table keeps a self-hosted
// dashboard byte-identical everywhere.
func contentType(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/vnd.microsoft.icon"
	default:
		return "application/octet-stream"
	}
}

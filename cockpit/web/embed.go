// Package web embeds the built Cockpit application so the loopback daemon
// serves it with no Node runtime and no network.
//
// dist/ is the production build of this directory's Angular project. It is
// git-ignored except for dist/.gitkeep, which exists because go:embed refuses
// a directory that is not there, so a wb built from a clean clone compiles
// and Handler serves a one-line page naming the build command.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Dist holds every file the Cockpit build emitted, or only .gitkeep in a
// clone where the build has never run.
//
//go:embed all:dist
var Dist embed.FS

// MountPath is where the application is served.
const MountPath = "/cockpit/"

// indexPage is the application's entry document; its presence is what tells
// Handler the dist is real rather than a placeholder.
const indexPage = "index.html"

const notBuiltPage = "Cockpit was not built into this wb binary. " +
	"Run `pnpm install && pnpm build` in cockpit/web, then rebuild wb.\n"

// distFS is dist/ as a filesystem of its own. fs.Sub only fails on a
// malformed path and "dist" is a literal go:embed has already resolved, so
// the error cannot happen and is discarded rather than turned into an
// unreachable branch.
var distFS, _ = fs.Sub(Dist, "dist")

func builtIn(files fs.FS) bool {
	_, err := fs.Stat(files, indexPage)
	return err == nil
}

// Handler serves the embedded application under MountPath, or the one-line
// not-built page when none is embedded. Mount it at MountPath; the prefix is
// stripped here so the caller does not have to.
func Handler() http.Handler { return HandlerFor(distFS) }

// HandlerFor is Handler over an injectable tree, so the built and not-built
// pages are both testable in a checkout that has only one of them.
//
// Only GET and HEAD are answered. A path with no file extension is a
// client-side route and gets the entry document; a path with an extension that
// names no file is a missing asset and gets 404, never HTML. The entry
// document, the fallback and the not-built page are sent no-cache so a new wb
// binary's application is never masked by a stale one.
func HandlerFor(files fs.FS) http.Handler {
	built := builtIn(files)
	return http.StripPrefix(strings.TrimSuffix(MountPath, "/"), http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			writer.Header().Set("Cache-Control", "no-cache")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write([]byte(notBuiltPage))
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
		if path.Base(name) == ".gitkeep" {
			http.NotFound(writer, request)
			return
		}
		info, err := fs.Stat(files, name)
		isFile := name != "" && err == nil && !info.IsDir()
		switch {
		case isFile:
		case name == "" || path.Ext(name) == "" || (err == nil && info.IsDir()):
			// A client-side route, or the base itself: the single-page
			// application owns it, so answer with the entry document.
			name = indexPage
		default:
			http.NotFound(writer, request)
			return
		}
		content, err := fs.ReadFile(files, name)
		if err != nil {
			http.NotFound(writer, request)
			return
		}
		if !isFile || name == indexPage {
			writer.Header().Set("Cache-Control", "no-cache")
		}
		writer.Header().Set("Content-Type", contentType(name))
		_, _ = writer.Write(content)
	}))
}

// contentType maps the extensions an Angular build emits. The standard
// library's mime package consults the host's /etc/mime.types, which makes the
// served type vary by machine; a fixed table keeps Cockpit identical
// everywhere.
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

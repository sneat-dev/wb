// Package web embeds the built Cockpit application so the loopback daemon
// serves it with no Node runtime and no network.
//
// dist/ is the production build of this directory's Angular project. It is
// git-ignored except for dist/.gitkeep, which exists because go:embed refuses
// a directory that is not there, so a wb built from a clean clone compiles
// and Handler serves a one-line page naming the build command.
package web

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"regexp"
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

// noncePlaceholder stands in for the style nonce in the built entry document
// (the ngCspNonce attribute on the application root). Handler replaces it with
// a fresh nonce on every response, so the nonce is never baked into the build.
const noncePlaceholder = "__CSP_NONCE__"

// PolicyFor is the content security policy of every response under MountPath.
// Scripts come only from the daemon's own origin: no unsafe-inline, no
// unsafe-eval. Styles come from the own origin plus the response's nonce, which
// covers the style elements Angular and PrimeNG inject at run time; style
// attributes in markup are not allowed. Images come only from the own origin
// (no data: URL, no foreign origin), so content that names an image elsewhere
// makes no request. Framing is limited to the own origin.
func PolicyFor(nonce string) string {
	return "default-src 'none'; script-src 'self'; style-src 'self' 'nonce-" + nonce + "'; " +
		"img-src 'self'; font-src 'self'; connect-src 'self'; " +
		"base-uri 'self'; form-action 'self'; frame-ancestors 'self'"
}

// FreshPolicy is PolicyFor with a new random nonce, for a response the
// application handler does not write: it is what lets the code that answers
// ahead of Handler carry the same policy shape.
func FreshPolicy() string { return PolicyFor(rand.Text()) }

// gzipExtension marks a file the build compressed beside the original
// (tools/lib/finish-build.mjs): `main-ABC.js` has `main-ABC.js.gz`. The
// compressed file is served only as the encoding of the original, never under
// its own name.
const gzipExtension = ".gz"

// hashedAsset matches the name of a content-hashed build output: a base name,
// a hyphen, the build's eight-character hash and an extension. Its content
// never changes under that name, so it may be cached for a year.
var hashedAsset = regexp.MustCompile(`-[A-Z0-9]{8}\.[A-Za-z0-9]+$`)

// immutableCache is the Cache-Control of a content-hashed asset.
const immutableCache = "public, max-age=31536000, immutable"

// gzipVary is the Vary value of a response whose encoding depends on the
// request; the cockpit page route has already set Origin.
const gzipVary = "Origin, Accept-Encoding"

// acceptsGzip reports whether the request lists gzip, or `*`, with a quality
// other than zero.
func acceptsGzip(request *http.Request) bool {
	for _, value := range request.Header.Values("Accept-Encoding") {
		for _, item := range strings.Split(value, ",") {
			name, parameters, _ := strings.Cut(item, ";")
			if name = strings.ToLower(strings.TrimSpace(name)); name != "gzip" && name != "*" {
				continue
			}
			if strings.ReplaceAll(strings.TrimSpace(parameters), " ", "") == "q=0" {
				continue
			}
			return true
		}
	}
	return false
}

// gzipBytes compresses a document that was changed for this response.
func gzipBytes(data []byte) []byte {
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	// Writing to a bytes.Buffer cannot fail.
	_, _ = writer.Write(data)
	_ = writer.Close()
	return buffer.Bytes()
}

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
//
// Every response carries the strict Content-Security-Policy, set here after any
// outer middleware so it is the policy the browser receives. The entry
// document gets a fresh random style nonce in the policy and in its markup.
func Handler() http.Handler { return HandlerFor(distFS) }

// HandlerFor is Handler over an injectable tree, so the built and not-built
// pages are both testable in a checkout that has only one of them.
//
// Only GET and HEAD are answered. A missing path whose extension is
// one this handler has a content type for is a missing asset and gets 404,
// never HTML; any other missing path, with or without a dot, is a client-side
// route and gets the entry document. The entry
// document, the fallback and the not-built page are sent no-cache so a new wb
// binary's application is never masked by a stale one.
func HandlerFor(files fs.FS) http.Handler {
	built := builtIn(files)
	return http.StripPrefix(strings.TrimSuffix(MountPath, "/"), http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		nonce := rand.Text()
		writer.Header().Set("Content-Security-Policy", PolicyFor(nonce))
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
		if path.Base(name) == ".gitkeep" || path.Ext(name) == gzipExtension {
			http.NotFound(writer, request)
			return
		}
		info, err := fs.Stat(files, name)
		isFile := name != "" && err == nil && !info.IsDir()
		switch {
		case isFile:
		case name == "" || (err == nil && info.IsDir()) || !knownExtension(path.Ext(name)):
			// A client-side route, or the base itself: the single-page
			// application owns it, so answer with the entry document. A dot
			// in the last segment (a version, a name) does not make it an
			// asset; only an extension the handler has a type for does.
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
		header := writer.Header()
		zipped := acceptsGzip(request)
		header.Set("Vary", gzipVary)
		switch {
		case !isFile || name == indexPage:
			header.Set("Cache-Control", "no-cache")
		case hashedAsset.MatchString(name):
			header.Set("Cache-Control", immutableCache)
		}
		if name == indexPage {
			// The nonce is per response, so the entry document is compressed per
			// response too, and never from a build-time file.
			content = bytes.ReplaceAll(content, []byte(noncePlaceholder), []byte(nonce))
			if zipped {
				content = gzipBytes(content)
			}
		} else if zipped {
			if precompressed, err := fs.ReadFile(files, name+gzipExtension); err == nil {
				content = precompressed
			} else {
				zipped = false
			}
		}
		if zipped {
			header.Set("Content-Encoding", "gzip")
		}
		header.Set("Content-Type", contentType(name))
		_, _ = writer.Write(content)
	}))
}

// contentTypes maps the extensions an Angular build emits. The standard
// library's mime package consults the host's /etc/mime.types, which makes the
// served type vary by machine; a fixed table keeps Cockpit identical
// everywhere.
var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".mjs":   "text/javascript; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".svg":   "image/svg+xml",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".png":   "image/png",
	".ico":   "image/vnd.microsoft.icon",
}

func knownExtension(extension string) bool {
	_, known := contentTypes[extension]
	return known
}

func contentType(name string) string {
	if kind, known := contentTypes[path.Ext(name)]; known {
		return kind
	}
	return "application/octet-stream"
}

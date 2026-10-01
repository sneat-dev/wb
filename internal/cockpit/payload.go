package cockpit

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// varyHeader is the Vary value of every Cockpit response whose bytes depend on
// the caller's origin or on the encodings it accepts.
const varyHeader = "Origin, Accept-Encoding"

// gzipSuffix marks a strong ETag as the one of the gzip encoding of a body.
const gzipSuffix = "-gzip"

// Gzip compresses data at the best level. It is the compressor a Payload is
// built with in production: the bytes are computed once, when the body is
// stored, so the level costs nothing per request.
func Gzip(data []byte) []byte {
	var buffer bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	// Writing to a bytes.Buffer cannot fail.
	_, _ = writer.Write(data)
	_ = writer.Close()
	return buffer.Bytes()
}

// Payload is a JSON body prepared once for every request that will read it:
// the identity bytes, their gzip encoding and the strong ETags of the two
// (cockpit-views#req:compressed-responses). Preparing it is the only place the
// compressor runs, so serving never compresses.
type Payload struct {
	identity []byte
	gzipped  []byte
	tag      string
}

// NewPayload prepares identity, compressing it once with compress.
func NewPayload(identity []byte, compress func([]byte) []byte) Payload {
	sum := sha256.Sum256(identity)
	return Payload{identity: identity, gzipped: compress(identity), tag: `"` + hex.EncodeToString(sum[:12]) + `"`}
}

// Identity returns the uncompressed bytes and their strong ETag.
func (payload Payload) Identity() (body []byte, etag string) { return payload.identity, payload.tag }

// gzipTag is the strong ETag of the gzip encoding.
func (payload Payload) gzipTag() string {
	return strings.TrimSuffix(payload.tag, `"`) + gzipSuffix + `"`
}

// ServePayload answers a GET with payload: gzip when the request accepts it,
// identity otherwise, each with its own strong ETag, `Vary: Origin,
// Accept-Encoding` and `Cache-Control: no-cache` (a client may keep the body
// but revalidates on every use). If-None-Match accepts either ETag form, and
// `*`, and a match is answered 304 with no body.
func ServePayload(writer http.ResponseWriter, request *http.Request, payload Payload) {
	header := writer.Header()
	header.Set("Vary", varyHeader)
	header.Set("Cache-Control", "no-cache")
	zipped := acceptsGzip(request.Header.Values("Accept-Encoding"))
	body, etag := payload.identity, payload.tag
	if zipped {
		body, etag = payload.gzipped, payload.gzipTag()
	}
	header.Set("ETag", etag)
	for _, candidate := range strings.Split(request.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
		if candidate == payload.tag || candidate == payload.gzipTag() || candidate == "*" {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if zipped {
		header.Set("Content-Encoding", "gzip")
	}
	_, _ = writer.Write(body)
}

// acceptsGzip reports whether an Accept-Encoding header lists gzip (or `*`)
// with a quality other than zero.
func acceptsGzip(values []string) bool {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			name, parameters, _ := strings.Cut(item, ";")
			if name = strings.ToLower(strings.TrimSpace(name)); name != "gzip" && name != "*" {
				continue
			}
			quality := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(parameters), "q="))
			if parsed, err := strconv.ParseFloat(quality, 64); strings.Contains(parameters, "q=") && err == nil && parsed == 0 {
				continue
			}
			return true
		}
	}
	return false
}

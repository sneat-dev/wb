package orchestrate

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"net/url"
	"strconv"
	"strings"
)

// The observer paginates but is not an endpoint-policy boundary. Every initial
// and next request is validated here BEFORE the captured underlying transport.
func legacyCommentPages(ctx context.Context, repository, candidate string) ([]githubobserver.Response, error) {
	path := "/repos/" + repository + "/commits/" + candidate + "/comments"
	wrapped := githubobserver.WithReader(ctx, legacyCommentPageReader(ctx, repository, path))
	return githubobserver.GetPages(wrapped, githubobserver.GetRequest{Repository: repository, Endpoint: "https://api.github.com" + path + "?page=1&per_page=100"}, 0)
}

// This adapter is the only transport path for the complete comment walk.
func legacyCommentPageReader(ctx context.Context, repository, path string) githubobserver.Reader {
	prior := ctx
	nextPage := 1
	announcedLast := 0
	return githubobserver.Reader{Get: func(call context.Context, r githubobserver.GetRequest) (githubobserver.Response, error) {
		if e := call.Err(); e != nil {
			return githubobserver.Response{}, e
		}
		endpoint, page, e := legacyPageURL(r.Endpoint, path)
		if e != nil {
			return githubobserver.Response{}, e
		}
		if page != nextPage || r.Repository != repository || len(r.Query) != 0 {
			return githubobserver.Response{}, fmt.Errorf("legacy comment pagination sequence or request scope changed")
		}
		r.Endpoint = endpoint
		response, e := githubobserver.Get(prior, r)
		if e != nil {
			return githubobserver.Response{}, e
		}
		if e = call.Err(); e != nil {
			return githubobserver.Response{}, e
		}
		if e = legacyPageLinksThrough(response.Headers, path, page, &announcedLast); e != nil {
			return githubobserver.Response{}, e
		}
		nextPage++
		return response, nil
	}}
}
func legacyPageURL(raw, path string) (string, int, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return "", 0, fmt.Errorf("invalid legacy comment page URL: %w", e)
	}
	if u.Scheme != "https" || u.Host != "api.github.com" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.Path != path || u.EscapedPath() != path {
		return "", 0, fmt.Errorf("foreign legacy comment page endpoint")
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return "", 0, fmt.Errorf("invalid legacy comment page query: %w", e)
	}
	if len(q) != 2 || len(q["page"]) != 1 || len(q["per_page"]) != 1 || q.Get("per_page") != "100" {
		return "", 0, fmt.Errorf("unsupported legacy comment pagination parameters")
	}
	page, e := strconv.Atoi(q.Get("page"))
	if e != nil || page < 1 || strconv.Itoa(page) != q.Get("page") {
		return "", 0, fmt.Errorf("invalid legacy comment page number")
	}
	for _, part := range strings.Split(u.RawQuery, "&") {
		key, value, ok := strings.Cut(part, "=")
		if !ok || key != "page" && key != "per_page" || value != q.Get(key) {
			return "", 0, fmt.Errorf("noncanonical legacy comment query")
		}
	}
	return u.String(), page, nil
}

// A later response may extend the boundary, but cannot erase or shrink an
// earlier promise of more inventory. State belongs to this one complete walk.
func legacyPageLinksThrough(headers map[string]string, path string, current int, announcedLast *int) error {
	next, last := 0, 0
	linkSeen := false
	for name, value := range headers {
		if !strings.EqualFold(strings.TrimSpace(name), "link") {
			continue
		}
		if linkSeen {
			return fmt.Errorf("ambiguous duplicate legacy comment Link headers")
		}
		linkSeen = true
		if strings.TrimSpace(value) == "" {
			continue
		}
		for _, section := range strings.Split(value, ",") {
			parts := strings.Split(section, ";")
			if len(parts) < 2 {
				return fmt.Errorf("incomplete legacy comment pagination link")
			}
			target := strings.TrimSpace(parts[0])
			if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
				return fmt.Errorf("malformed legacy comment pagination link")
			}
			_, page, e := legacyPageURL(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"), path)
			if e != nil {
				return e
			}
			relSeen := false
			for _, part := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
				if !ok || key != "rel" || relSeen || !strings.HasPrefix(value, "\"") || !strings.HasSuffix(value, "\"") {
					return fmt.Errorf("unsupported legacy comment link relation")
				}
				relSeen = true
				relations := strings.Fields(strings.Trim(value, "\""))
				if len(relations) != 1 || value != "\""+relations[0]+"\"" {
					return fmt.Errorf("unsupported legacy comment multi-relation link")
				}
				for _, rel := range relations {
					switch rel {
					case "next":
						if next != 0 {
							return fmt.Errorf("duplicate legacy comment next link")
						}
						next = page
					case "first", "prev":
						if page > current {
							return fmt.Errorf("invalid legacy comment previous page")
						}
					case "last":
						if last != 0 {
							return fmt.Errorf("duplicate legacy comment last link")
						}
						last = page
					default:
						return fmt.Errorf("unknown legacy comment link relation")
					}
				}
			}
		}
	}
	if next != 0 && next != current+1 || last > current && next == 0 || last != 0 && (next > last || last < current) {
		return fmt.Errorf("incomplete legacy comment pagination sequence")
	}
	if last != 0 && last < *announcedLast {
		return fmt.Errorf("legacy comment last page shrank during complete inventory")
	}
	if last > *announcedLast {
		*announcedLast = last
	}
	if next == 0 && current < *announcedLast {
		return fmt.Errorf("legacy comment inventory ended before its announced last page")
	}
	return nil
}

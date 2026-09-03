package githubobserver

import (
	"context"
	"fmt"
	"strings"
)

// DefaultMaxPages bounds a paginated read. GitHub's own maximum page size is
// 100, so this is 10 000 items — far past anything WB reads — and exists only
// so a malformed or looping `link` header cannot make a verb run forever.
const DefaultMaxPages = 100

// GetPages performs a paginated GET and returns one response per page, in
// order.
//
// It follows GitHub's own `link: <…>; rel="next"` header rather than asking the
// CLI to paginate. That matters because `gh api --paginate --slurp` — the
// obvious way to get one JSON document per page — needs a `gh` newer than the
// 2.45 installed on this fleet, and a land verb that breaks on the installed
// client sends operators back to raw `gh pr merge`, which is precisely how the
// cleanup that should have run at landing stopped running.
//
// Every page goes through the ordinary observer, so each is conditionally
// requested, cached, and throttle-aware exactly like a single-page read.
func GetPages(ctx context.Context, request GetRequest, maxPages int) ([]Response, error) {
	return Default().GetPages(ctx, request, maxPages)
}

func (o *Observer) GetPages(ctx context.Context, request GetRequest, maxPages int) ([]Response, error) {
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	responses := make([]Response, 0, 1)
	seen := map[string]bool{}
	endpoint := strings.TrimSpace(request.Endpoint)
	for page := 0; page < maxPages; page++ {
		if endpoint == "" {
			break
		}
		if seen[endpoint] {
			return nil, fmt.Errorf("GitHub pagination revisited %s; refusing to loop", endpoint)
		}
		seen[endpoint] = true
		pageRequest := request
		pageRequest.Endpoint = endpoint
		if page > 0 {
			// The next link already carries every query parameter GitHub wants
			// repeated; re-adding them would produce a different URL for the
			// same page and defeat the loop guard above.
			pageRequest.Query = nil
		}
		response, err := o.Get(ctx, pageRequest)
		if err != nil {
			return nil, err
		}
		responses = append(responses, response)
		endpoint = NextPageEndpoint(response.Headers)
	}
	if endpoint != "" {
		return nil, fmt.Errorf("GitHub pagination exceeded %d pages at %s", maxPages, endpoint)
	}
	return responses, nil
}

// NextPageEndpoint extracts the rel="next" URL from a GitHub link header. It
// returns "" when there is no next page, which is the ordinary end of a walk.
func NextPageEndpoint(headers map[string]string) string {
	link := ""
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), "link") {
			link = value
			break
		}
	}
	if strings.TrimSpace(link) == "" {
		return ""
	}
	for _, section := range strings.Split(link, ",") {
		parts := strings.Split(section, ";")
		if len(parts) < 2 {
			continue
		}
		target := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		for _, parameter := range parts[1:] {
			parameter = strings.TrimSpace(parameter)
			parameter = strings.ReplaceAll(parameter, `"`, "")
			parameter = strings.ReplaceAll(parameter, " ", "")
			if strings.EqualFold(parameter, "rel=next") {
				return strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">")
			}
		}
	}
	return ""
}

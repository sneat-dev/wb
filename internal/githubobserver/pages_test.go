package githubobserver

import (
	"context"
	"strings"
	"testing"
)

func pageResponse(body, link string) commandResult {
	header := "HTTP/2 200 OK\nContent-Type: application/json\n"
	if link != "" {
		header += "Link: " + link + "\n"
	}
	return commandResult{Stdout: []byte(header + "\n" + body)}
}

func TestGetPagesFollowsTheLinkHeaderWithoutSlurp(t *testing.T) {
	var requested []string
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, args ...string) commandResult {
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "--slurp") || strings.Contains(joined, "--paginate") {
				t.Fatalf("pagination must not depend on newer gh CLI behaviour: %v", args)
			}
			requested = append(requested, args[1])
			switch args[1] {
			case "repos/acme/app/rules/branches/main":
				return pageResponse(`[{"type":"required_status_checks"}]`,
					`<https://api.github.com/repos/acme/app/rules/branches/main?page=2>; rel="next", <https://api.github.com/repos/acme/app/rules/branches/main?page=2>; rel="last"`)
			case "https://api.github.com/repos/acme/app/rules/branches/main?page=2":
				return pageResponse(`[{"type":"merge_queue"}]`, "")
			}
			t.Fatalf("unexpected endpoint %q", args[1])
			return commandResult{}
		},
	}

	pages, err := observer.GetPages(context.Background(), GetRequest{
		Repository: "acme/app",
		Endpoint:   "repos/acme/app/rules/branches/main",
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(pages))
	}
	if string(pages[0].Body) != `[{"type":"required_status_checks"}]` || string(pages[1].Body) != `[{"type":"merge_queue"}]` {
		t.Fatalf("page bodies = %q, %q", pages[0].Body, pages[1].Body)
	}
	if len(requested) != 2 {
		t.Fatalf("requested = %v", requested)
	}
}

func TestGetPagesReturnsOnePageWhenThereIsNoNextLink(t *testing.T) {
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return pageResponse(`[]`, `<https://api.github.com/x?page=1>; rel="first"`)
		},
	}
	pages, err := observer.GetPages(context.Background(), GetRequest{Endpoint: "repos/acme/app/x"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || string(pages[0].Body) != "[]" {
		t.Fatalf("pages = %#v", pages)
	}
}

// A link header that points back at a page already read would otherwise make a
// verb walk forever, and a wait that never ends is indistinguishable from a
// hang.
func TestGetPagesRefusesALoopingLinkHeader(t *testing.T) {
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			return pageResponse(`[]`, `<repos/acme/app/x>; rel="next"`)
		},
	}
	if _, err := observer.GetPages(context.Background(), GetRequest{Endpoint: "repos/acme/app/x"}, 0); err == nil ||
		!strings.Contains(err.Error(), "refusing to loop") {
		t.Fatalf("err = %v, want a loop refusal", err)
	}
}

func TestGetPagesRefusesToExceedItsBound(t *testing.T) {
	page := 0
	observer := &Observer{
		StateDir: t.TempDir(),
		Run: func(_ context.Context, _ string, _ ...string) commandResult {
			page++
			return pageResponse(`[]`, `<https://api.github.com/x?page=`+itoa(page+1)+`>; rel="next"`)
		},
	}
	if _, err := observer.GetPages(context.Background(), GetRequest{Endpoint: "repos/acme/app/x"}, 2); err == nil ||
		!strings.Contains(err.Error(), "exceeded 2 pages") {
		t.Fatalf("err = %v, want a bound refusal", err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func TestNextPageEndpointReadsOnlyTheNextRelation(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		headers map[string]string
		want    string
	}{
		{"absent", map[string]string{}, ""},
		{"empty", map[string]string{"link": "  "}, ""},
		{"next", map[string]string{"link": `<https://api.github.com/a?page=2>; rel="next"`}, "https://api.github.com/a?page=2"},
		{"last only", map[string]string{"link": `<https://api.github.com/a?page=9>; rel="last"`}, ""},
		{"mixed case", map[string]string{"Link": `<https://api.github.com/a?page=3>; rel=next`}, "https://api.github.com/a?page=3"},
		{"malformed", map[string]string{"link": "not a link header"}, ""},
	} {
		if got := NextPageEndpoint(testCase.headers); got != testCase.want {
			t.Errorf("%s: NextPageEndpoint = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

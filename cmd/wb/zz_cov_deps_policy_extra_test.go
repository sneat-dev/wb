package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCwDepsFetchPolicyOverHTTP(t *testing.T) {
	document := testPolicyDocument
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/policy.yaml":
			writer.Header().Set("Content-Type", "application/yaml")
			_, _ = writer.Write([]byte(document))
		case "/missing.yaml":
			writer.WriteHeader(http.StatusNotFound)
		default:
			writer.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	path, err := fetchPolicy(server.URL + "/policy.yaml")
	if err != nil {
		t.Fatalf("fetchPolicy: %v", err)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read fetched policy: %v", readErr)
	}
	if string(raw) != document {
		t.Errorf("fetched policy = %q, want the served document", string(raw))
	}
	if !strings.HasPrefix(filepath.Base(path), "wb-policy-") {
		t.Errorf("fetched policy path = %q, want a wb-policy temp file", path)
	}
	// A non-200 response surfaces the status rather than an empty document.
	if _, err := fetchPolicy(server.URL + "/missing.yaml"); err == nil ||
		!strings.Contains(err.Error(), "404") {
		t.Fatalf("non-200 fetch = %v", err)
	}
	// A transport failure names the URL it could not reach.
	if _, err := fetchPolicy("http://127.0.0.1:1/policy.yaml"); err == nil ||
		!strings.Contains(err.Error(), "fetch policy") {
		t.Fatalf("unreachable fetch = %v", err)
	}
}

func TestCwDepsResolvePolicyRefusesAnUnfetchableURLSource(t *testing.T) {
	module := t.TempDir()
	cwCovWriteFile(t, filepath.Join(module, "go.mod"), "module github.com/acme/app/backend\n\ngo 1.26\n")
	// An https policy source takes the fetch branch; an unreachable one is a
	// usage error naming the fetch, never a silent fallback to no policy.
	_, err := resolvePolicy(&invocation{}, module, "https://127.0.0.1:1/policy.yaml")
	if exitCodeOfSafe(err) != exitUsage || !strings.Contains(err.Error(), "fetch policy") {
		t.Fatalf("unfetchable policy source = %v", err)
	}
	// An http source is refused before any request is attempted, because a
	// policy fetched in the clear is not the policy a repository agreed to.
	if _, err := resolvePolicy(&invocation{}, module, "http://example.test/policy.yaml"); exitCodeOfSafe(err) != exitUsage ||
		!strings.Contains(err.Error(), "must use https") {
		t.Fatalf("insecure policy source = %v", err)
	}
}

func TestCwDepsPolicySearchRootsAndModuleDir(t *testing.T) {
	if roots := policySearchRoots(&invocation{}); roots != nil {
		t.Errorf("policySearchRoots with no projects root = %v, want nil", roots)
	}
	projectsRoot := t.TempDir()
	if roots := policySearchRoots(&invocation{projectsRoot: projectsRoot}); len(roots) != 1 || roots[0] != projectsRoot {
		t.Errorf("policySearchRoots = %v", roots)
	}

	// findModuleDir walks up to the owning go.mod.
	root := t.TempDir()
	cwCovWriteFile(t, filepath.Join(root, "go.mod"), "module github.com/acme/app\n\ngo 1.26\n")
	nested := filepath.Join(root, "backend", "internal", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := findModuleDir(nested)
	if err != nil || found != root {
		t.Fatalf("findModuleDir = %q, %v", found, err)
	}
	// A directory under no module reports the path it started from.
	orphan := t.TempDir()
	if _, err := findModuleDir(orphan); err == nil ||
		!strings.Contains(err.Error(), "no go.mod found at or above") {
		t.Fatalf("findModuleDir outside a module = %v", err)
	}
}

func TestCwDepsOrNoneAndDirectoryArg(t *testing.T) {
	if got := orNone(""); got != "nothing" {
		t.Errorf("orNone(empty) = %q", got)
	}
	if got := orNone("acme/policy//p.yaml"); got != "acme/policy//p.yaml" {
		t.Errorf("orNone(value) = %q", got)
	}
	if got := directoryArg(nil); got != "." {
		t.Errorf("directoryArg(nil) = %q", got)
	}
	if got := directoryArg([]string{""}); got != "." {
		t.Errorf("directoryArg(empty) = %q", got)
	}
	if got := directoryArg([]string{"/tmp/module"}); got != "/tmp/module" {
		t.Errorf("directoryArg(path) = %q", got)
	}
}

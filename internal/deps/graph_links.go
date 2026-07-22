package deps

import (
	"net/url"
	"strings"
)

const (
	codeGrapherHost = "codegrapher.dev"
	githubHost      = "github.com"
)

func graphRepositoryLinks(repository string) (githubURL, codeGrapherURL string) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repository), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", ""
	}
	githubURL = (&url.URL{Scheme: "https", Host: githubHost, Path: "/" + owner + "/" + name}).String()
	codeGrapherURL = (&url.URL{Scheme: "https", Host: codeGrapherHost, Path: "/" + githubHost + "/" + owner + "/" + name}).String()
	return githubURL, codeGrapherURL
}

func graphRepositoryOrganization(repository string) string {
	organization, _, ok := strings.Cut(repository, "/")
	if !ok {
		return ""
	}
	return organization
}

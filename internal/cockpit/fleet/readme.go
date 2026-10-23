package fleet

import (
	"context"
	"errors"
	"net/http"
)

// readmeFailure maps a README read's error to the status and short code the
// route answers, which never carry the error's own text.
func readmeFailure(err error) (status int, code string) {
	switch {
	case errors.Is(err, errReadmeAbsent):
		return http.StatusNotFound, "readme_not_found"
	case errors.Is(err, errReadmeNotRegular):
		return http.StatusForbidden, "readme_not_a_regular_file"
	case errors.Is(err, errReadmeTooLarge):
		return http.StatusRequestEntityTooLarge, "readme_too_large"
	default:
		return http.StatusInternalServerError, "read_failed"
	}
}

// readme reads the README of the repository with id, committed at the tip of
// its default branch, under the repository timeout. The first result is the
// HTTP status to answer when the read does not succeed, zero when it does.
func (s *Snapshotter) readme(ctx context.Context, id string) (data []byte, status int, code string) {
	repo, branch, found := s.readmeTarget(id)
	switch {
	case !found:
		return nil, http.StatusNotFound, "unknown_repository"
	case s.gitTooOld():
		return nil, http.StatusServiceUnavailable, ErrorGitTooOld
	case branch == "":
		return nil, http.StatusNotFound, "default_branch_unknown"
	}
	ctx, cancel := context.WithTimeout(ctx, s.repositoryTimeout)
	defer cancel()
	data, err := s.collectors.Readme.Readme(ctx, repo, branch)
	if err != nil {
		status, code = readmeFailure(err)
		return nil, status, code
	}
	return data, 0, ""
}

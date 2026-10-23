package orchestrate

import (
	"context"
	"strings"

	"github.com/sneat-dev/wb/internal/githubobserver"
)

func githubRead(ctx context.Context, worktree string, args ...string) (string, error) {
	output, err := githubobserver.Read(ctx, worktree, args...)
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func githubGet(ctx context.Context, worktree, repository, target, head, endpoint string) ([]byte, error) {
	response, err := githubobserver.Get(ctx, githubobserver.GetRequest{
		Dir:         worktree,
		Repository:  strings.TrimSpace(repository),
		Target:      strings.TrimSpace(target),
		Head:        strings.TrimSpace(head),
		Endpoint:    endpoint,
		FreshWindow: 0,
	})
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

func githubExecute(ctx context.Context, worktree string, args ...string) githubobserver.CommandResponse {
	return githubobserver.Execute(ctx, worktree, args...)
}

package locallink

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/streams"
)

// linkNpm builds the library once and asks the Node port to stage its dist in
// the consumer's installed peer context before linking it into node_modules.
//
// The build is cached against the library's **content hash** and rebuilt
// whenever that hash moves. Building once and reusing it across an iterative
// stream would have consumers verifying against a stale `dist` and reporting
// false green — the failure this link exists to prevent.
//
// A clean frozen install of the *unlinked* tree runs first, so a link never
// masks a lockfile or manifest mismatch.
func (engine *Engine) linkNpm(
	ctx context.Context,
	library, consumer string,
	declaration streams.Declaration,
	libraryRepository, hash string,
) (streams.Link, error) {
	// The frozen install is NOT run here. It proves a clean install of the
	// unlinked tree, so it belongs once per consumer before any linking —
	// linkConsumer owns it. Running it per identity meant every install after
	// the first ran against an already-linked tree, and a real
	// `pnpm install --frozen-lockfile` would reconcile node_modules against
	// the lockfile and remove the link it was supposed to be validating.
	if engine.Node == nil {
		return streams.Link{}, fmt.Errorf("no Node toolchain available to link %s", declaration.Identity.Name)
	}
	libraryWorkspace, err := workspacePath(library, declaration.Identity.Workspace)
	if err != nil {
		return streams.Link{}, err
	}
	consumerWorkspace, err := workspacePath(consumer, declaration.Workspace)
	if err != nil {
		return streams.Link{}, err
	}
	packageDir := library
	if declaration.Identity.Directory != "." {
		packageDir = filepath.Join(library, filepath.FromSlash(declaration.Identity.Directory))
	}
	dist, err := engine.Node.Build(ctx, libraryWorkspace, packageDir)
	if err != nil {
		return streams.Link{}, fmt.Errorf("build %s with the repository's own build target: %w", declaration.Identity.Name, err)
	}
	linkResult, err := engine.Node.Link(ctx, consumerWorkspace, declaration.Identity.Name, dist)
	if err != nil {
		return streams.Link{}, fmt.Errorf("link %s into %s: %w", declaration.Identity.Name, consumer, err)
	}
	artifacts := make([]string, 0, len(linkResult.Artifacts))
	for _, artifact := range linkResult.Artifacts {
		artifacts = append(artifacts, filepath.ToSlash(filepath.Clean(filepath.Join(filepath.FromSlash(declaration.Workspace), filepath.FromSlash(artifact)))))
	}
	if len(artifacts) == 0 {
		artifact := filepath.Join(filepath.FromSlash(declaration.Workspace), "node_modules", filepath.FromSlash(declaration.Identity.Name))
		artifacts = append(artifacts, filepath.ToSlash(filepath.Clean(artifact)))
	}
	if linkResult.Previous != "" {
		previous := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.FromSlash(declaration.Workspace), filepath.FromSlash(linkResult.Previous))))
		if !containsString(artifacts, previous) {
			artifacts = append(artifacts, previous)
		}
	}
	return streams.Link{
		Library:           library,
		LibraryRepository: libraryRepository,
		Mechanism:         streams.MechanismPnpmLink,
		State:             streams.LinkStateApplied,
		Identity:          declaration.Identity.Name,
		PreviousVersion:   declaration.Version,
		ContentHash:       hash,
		Artifacts:         artifacts,
		Workspace:         declaration.Workspace,
		CreatedAt:         engine.now(),
	}, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

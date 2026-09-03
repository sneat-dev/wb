package locallink

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/streams"
)

// linkNpm builds the library once and links its built dist into the consumer's
// node_modules.
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
	options Options,
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
	packageDir := library
	if declaration.Identity.Directory != "." {
		packageDir = filepath.Join(library, filepath.FromSlash(declaration.Identity.Directory))
	}
	dist, err := engine.Node.Build(ctx, library, packageDir)
	if err != nil {
		return streams.Link{}, fmt.Errorf("build %s with the repository's own build target: %w", declaration.Identity.Name, err)
	}
	previous, err := engine.Node.Link(ctx, consumer, declaration.Identity.Name, dist)
	if err != nil {
		return streams.Link{}, fmt.Errorf("link %s into %s: %w", declaration.Identity.Name, consumer, err)
	}
	artifacts := []string{filepath.ToSlash(filepath.Join("node_modules", filepath.FromSlash(declaration.Identity.Name)))}
	if previous != "" {
		artifacts = append(artifacts, previous)
	}
	return streams.Link{
		Library:           library,
		LibraryRepository: libraryRepository,
		Mechanism:         streams.MechanismPnpmLink,
		Identity:          declaration.Identity.Name,
		PreviousVersion:   declaration.Version,
		ContentHash:       hash,
		Artifacts:         artifacts,
		CreatedAt:         engine.now(),
	}, nil
}

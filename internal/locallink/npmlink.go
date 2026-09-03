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
	if engine.Node == nil {
		return streams.Link{}, fmt.Errorf("no Node toolchain available to link %s", declaration.Identity.Name)
	}
	if err := engine.Node.FrozenInstall(ctx, consumer); err != nil {
		return streams.Link{}, fmt.Errorf("prove a clean frozen install of %s before linking: %w", consumer, err)
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

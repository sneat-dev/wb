package wbskills

import (
	"io/fs"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/strongo/cli-helpers/skillsync"
)

const LegacyMarker = ".wb-skills-sync.json"
const PluginVersion = "0.0.0"
const UnknownSource = "0000000000000000000000000000000000000000"

func CLIIdentity() skillsync.Identity { return skillsync.Identity{Publisher: "sneat-dev", Name: "wb"} }
func PluginIdentity() skillsync.PluginIdentity {
	return skillsync.PluginIdentity{Publisher: "sneat-dev", Name: "wb"}
}
func Config(sourceFS fs.FS, build buildinfo.Report) (skillsync.Config, error) {
	source, err := fs.Sub(sourceFS, "skills")
	if err != nil {
		return skillsync.Config{}, err
	}
	digest, err := skillsync.Digest(source)
	if err != nil {
		return skillsync.Config{}, err
	}
	revision := build.Revision
	if len(revision) != 40 {
		revision = UnknownSource
	}
	pluginVersion := build.Version
	if _, err := skillsync.CompareVersions(pluginVersion, pluginVersion); err != nil {
		pluginVersion = PluginVersion
	}
	bundle, err := skillsync.EmbeddedBundle(skillsync.BundleDescriptor{
		Plugin: PluginIdentity(),
		Source: skillsync.Source{
			Repository: "github.com/sneat-dev/wb",
			Path:       "ai/skills",
			Revision:   revision,
			Version:    pluginVersion,
			Digest:     digest,
		},
	}, source)
	if err != nil {
		return skillsync.Config{}, err
	}
	return skillsync.Config{
		CLI:            CLIIdentity(),
		CurrentVersion: build.Version,
		Bundles:        []skillsync.Bundle{bundle},
	}, nil
}

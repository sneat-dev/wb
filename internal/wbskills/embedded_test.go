package wbskills

import (
	"testing"

	"github.com/sneat-dev/wb/ai"
	"github.com/sneat-dev/wb/internal/buildinfo"
)

func TestNewSkillsSyncConfigBindsTheEmbeddedWBPluginToThisBuild(t *testing.T) {
	t.Parallel()
	cfg, err := Config(ai.SkillsFS, buildinfo.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLI != CLIIdentity() || cfg.CurrentVersion != buildinfo.Snapshot().Version {
		t.Fatalf("CLI config = %+v @ %q", cfg.CLI, cfg.CurrentVersion)
	}
	if len(cfg.Bundles) != 1 {
		t.Fatalf("bundles = %d, want one WB plugin", len(cfg.Bundles))
	}
	bundle := cfg.Bundles[0]
	if bundle.Plugin != PluginIdentity() {
		t.Errorf("plugin = %+v, want %+v", bundle.Plugin, PluginIdentity())
	}
	if bundle.Source.Repository != "github.com/sneat-dev/wb" || bundle.Source.Path != "ai/skills" {
		t.Errorf("source = %+v", bundle.Source)
	}
	wantRevision := buildinfo.Snapshot().Revision
	if len(wantRevision) != 40 {
		wantRevision = UnknownSource
	}
	if bundle.Source.Revision != wantRevision {
		t.Errorf("revision = %q, want %q", bundle.Source.Revision, wantRevision)
	}
	if bundle.Source.Digest == "" {
		t.Error("embedded plugin digest is empty")
	}

}

package wbskills

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/strongo/cli-helpers/skillsync"
)

type brokenFS struct {
	err error
	sub bool
}

func (f brokenFS) Open(string) (fs.File, error) { return nil, f.err }
func (f brokenFS) Sub(string) (fs.FS, error) {
	if f.sub {
		return nil, f.err
	}
	return f, nil
}

func TestEmbeddedConfigFailuresAndVersionFallbacks(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("source unavailable")
	for _, sub := range []bool{false, true} {
		if _, err := Config(brokenFS{err: sentinel, sub: sub}, buildinfo.Report{Version: "1.2.3"}); !errors.Is(err, sentinel) {
			t.Fatalf("sub=%v err=%v", sub, err)
		}
	}
	if _, err := Config(fstest.MapFS{"skills/not-a-skill.txt": {Data: []byte("invalid bundle")}}, buildinfo.Report{Version: "1.2.3"}); err == nil {
		t.Fatal("empty skills bundle accepted")
	}
	source := fstest.MapFS{"skills/fixture/SKILL.md": {Data: []byte("---\nname: fixture\ndescription: fixture skill\n---\n# Fixture\n")}}
	for _, version := range []string{"1.2.3", "unknown"} {
		cfg, err := Config(source, buildinfo.Report{Version: version, Revision: strings.Repeat("a", 40)})
		if err != nil {
			t.Fatal(err)
		}
		want := version
		if version == "unknown" {
			want = PluginVersion
		}
		if cfg.Bundles[0].Source.Version != want || cfg.Bundles[0].Source.Revision != strings.Repeat("a", 40) || cfg.CurrentVersion != version {
			t.Fatalf("cfg=%+v", cfg)
		}
	}
}

func TestAnnouncementUsesIsolatedFunctionsAndFailsOpen(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"home-error", "status-error", "unknown", "devel", "empty", "current", "stale"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			calls := 0
			deps := AnnouncementDependencies{SkillsDir: func() (string, error) {
				if kind == "home-error" {
					return "", errors.New("no home")
				}
				return "/private/skills", nil
			}, ReadStatus: func(dir string) (skillsync.Status, error) {
				calls++
				if dir != "/private/skills" {
					t.Fatal(dir)
				}
				if kind == "status-error" {
					return skillsync.Status{}, errors.New("bad marker")
				}
				return skillsync.Status{Installed: true, Plugins: map[string]skillsync.Source{PluginIdentity().String(): {}}, SupplierCLIVersions: map[string]map[string]string{PluginIdentity().String(): {CLIIdentity().String(): "1.2.3"}}}, nil
			}, Version: func() string {
				switch kind {
				case "unknown":
					return "unknown"
				case "devel":
					return "(devel)"
				case "empty":
					return ""
				case "stale":
					return "1.2.4"
				}
				return "1.2.3"
			}}
			got := Announcement(deps)
			if !strings.Contains(got, "wb session register") {
				t.Fatal(got)
			}
			if strings.Contains(got, "wb skills sync") != (kind == "stale") {
				t.Fatalf("kind=%s got=%s", kind, got)
			}
			if kind == "home-error" && calls != 0 {
				t.Fatal("status read despite home error")
			}
		})
	}
}

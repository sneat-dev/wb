package sessionpark

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
)

func TestSpCovNewIDIsUniquePrivateRandomIdentity(t *testing.T) {
	t.Parallel()
	first, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "park-") || len(first) != len("park-")+32 || first == second {
		t.Fatalf("generated IDs first=%q second=%q", first, second)
	}
	if !validParkID(first) {
		t.Fatalf("generated ID %q is not a valid park identity", first)
	}
}

func TestSpCovValidateBundleRejectsEveryIncompleteField(t *testing.T) {
	t.Parallel()
	base := testBundle(t)
	remoteBase := remoteTestBundle(t)
	for name, mutate := range map[string]func(*Bundle){
		"schema":                func(value *Bundle) { value.SchemaVersion = 0 },
		"parked id":             func(value *Bundle) { value.ParkedSessionID = "../escape" },
		"zero source pid":       func(value *Bundle) { value.Source.PID = 0 },
		"invalid source id":     func(value *Bundle) { value.Source.WBSessionID = ".." },
		"invalid machine":       func(value *Bundle) { value.Source.Machine = ".." },
		"blank runtime":         func(value *Bundle) { value.Source.Runtime = "  " },
		"zero started at":       func(value *Bundle) { value.Source.StartedAt = time.Time{} },
		"zero parked at":        func(value *Bundle) { value.ParkedAt = time.Time{} },
		"empty continuation":    func(value *Bundle) { value.Continuation = "" },
		"oversize continuation": func(value *Bundle) { value.Continuation = strings.Repeat("c", MaxContinuationBytes+1) },
		"invalid utf8":          func(value *Bundle) { value.Continuation = "\xff\xfe" },
		"too many worktrees":    func(value *Bundle) { value.Worktrees = make([]Worktree, maxParkedWorktrees+1) },
		"blank repository":      func(value *Bundle) { value.Worktrees[0].Repository = "  " },
		"newline repository":    func(value *Bundle) { value.Worktrees[0].Repository = "acme/app\n" },
		"relative worktree":     func(value *Bundle) { value.Worktrees[0].WorktreeDir = "relative/dir" },
		"unclean worktree":      func(value *Bundle) { value.Worktrees[0].WorktreeDir = "/tmp/../escape" },
		"blank branch":          func(value *Bundle) { value.Worktrees[0].Branch = "" },
		"short head":            func(value *Bundle) { value.Worktrees[0].Head = "abc" },
		"duplicate worktree":    func(value *Bundle) { value.Worktrees[1].WorktreeDir = value.Worktrees[0].WorktreeDir },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := base
			candidate.Worktrees = append([]Worktree(nil), base.Worktrees...)
			mutate(&candidate)
			if _, err := EncodeBundle(candidate); err == nil {
				t.Fatalf("invalid bundle accepted: %#v", candidate)
			}
		})
	}
	for name, mutate := range map[string]func(*Bundle){
		"unsafe remote":      func(value *Bundle) { value.Worktrees[0].RepositoryRemote = "-oProxyCommand=boom" },
		"remote mismatch":    func(value *Bundle) { value.Worktrees[0].RepositoryRemote = "https://github.com/acme/other.git" },
		"short remote head":  func(value *Bundle) { value.Worktrees[0].RemoteHead = "abc" },
		"bad work log":       func(value *Bundle) { value.Worktrees[0].WorkLogReference = "bogus" },
		"bad owner event":    func(value *Bundle) { value.Worktrees[0].OwnerEventID = strings.Repeat("c", 63) },
		"uppercase owner":    func(value *Bundle) { value.Worktrees[0].OwnerEventID = strings.ToUpper(strings.Repeat("c", 64)) },
		"blank remote value": func(value *Bundle) { value.Worktrees[0].RepositoryRemote = "  " },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := remoteBase
			candidate.Worktrees = append([]Worktree(nil), remoteBase.Worktrees...)
			mutate(&candidate)
			if _, err := EncodeBundle(candidate); err == nil {
				t.Fatalf("invalid remote bundle accepted: %#v", candidate.Worktrees[0])
			}
		})
	}
	if _, err := EncodeBundle(remoteBase); err != nil {
		t.Fatalf("valid remote bundle rejected: %v", err)
	}
	if _, err := EncodeBundle(base); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
}

func TestSpCovBundleCodecRoundTripBoundsAndCanonicalBytes(t *testing.T) {
	t.Parallel()
	bundle := testBundle(t)
	raw, err := EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("canonical bundle is not newline terminated")
	}
	decoded, err := DecodeBundle(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !EqualBundle(bundle, decoded) {
		t.Fatalf("round trip changed the bundle: %#v", decoded)
	}

	t.Run("empty and oversized rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeBundle(nil); err == nil {
			t.Fatal("empty bundle decoded")
		}
		if _, err := DecodeBundle(make([]byte, MaxBundleBytes+1)); err == nil {
			t.Fatal("oversized bundle decoded")
		}
	})
	t.Run("malformed JSON rejected", func(t *testing.T) {
		t.Parallel()
		if _, err := DecodeBundle([]byte("{not json")); err == nil {
			t.Fatal("malformed bundle decoded")
		}
	})
	t.Run("unknown field rejected", func(t *testing.T) {
		t.Parallel()
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		value["unexpected"] = true
		unknown, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeBundle(unknown); err == nil {
			t.Fatal("unknown bundle field decoded")
		}
	})
	t.Run("invalid bundle rejected after decode", func(t *testing.T) {
		t.Parallel()
		invalid := bundle
		invalid.Source.PID = 0
		encoded, err := json.Marshal(invalid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeBundle(encoded); err == nil {
			t.Fatal("invalid bundle decoded")
		}
	})
	t.Run("noncanonical encoding rejected", func(t *testing.T) {
		t.Parallel()
		compact, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeBundle(compact); err == nil {
			t.Fatal("compact noncanonical bundle decoded")
		}
	})
	t.Run("oversized encoding rejected", func(t *testing.T) {
		t.Parallel()
		huge := bundle
		huge.Worktrees = append([]Worktree(nil), bundle.Worktrees...)
		huge.Worktrees[0].WorktreeDir = "/" + strings.Repeat("d", MaxBundleBytes+1024)
		if _, err := EncodeBundle(huge); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized bundle error = %v", err)
		}
	})
	t.Run("equal bundle distinguishes content", func(t *testing.T) {
		t.Parallel()
		other := bundle
		other.Worktrees = append([]Worktree(nil), bundle.Worktrees...)
		other.Continuation = "a different continuation"
		if EqualBundle(bundle, other) {
			t.Fatal("distinct bundles compared equal")
		}
	})
}

func TestSpCovCreateRejectsInvalidAndDuplicateAggregates(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	bundle := testBundle(t)
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(bundle); err == nil || !strings.Contains(err.Error(), "create parked session aggregate") {
		t.Fatalf("duplicate aggregate error = %v", err)
	}

	t.Run("invalid bundle", func(t *testing.T) {
		t.Parallel()
		invalid := testBundle(t)
		invalid.ParkedSessionID = "not-a-park-id"
		if _, err := NewStore(t.TempDir()).Create(invalid); err == nil {
			t.Fatal("invalid bundle created")
		}
	})
	t.Run("unencodable bundle", func(t *testing.T) {
		t.Parallel()
		huge := testBundle(t)
		huge.Worktrees = append([]Worktree(nil), huge.Worktrees...)
		huge.Worktrees[0].WorktreeDir = "/" + strings.Repeat("e", MaxBundleBytes+1024)
		if _, err := NewStore(t.TempDir()).Create(huge); err == nil {
			t.Fatal("unencodable bundle created")
		}
	})
	t.Run("unusable store root", func(t *testing.T) {
		t.Parallel()
		if _, err := NewStore("relative-store").Create(testBundle(t)); err == nil {
			t.Fatal("relative store root accepted")
		}
	})
}

func TestSpCovFindBySourceRepairsProjectsAndRejectsForeignArtifacts(t *testing.T) {
	t.Parallel()
	t.Run("invalid source id", func(t *testing.T) {
		t.Parallel()
		if _, _, err := NewStore(t.TempDir()).FindBySource(".."); err == nil {
			t.Fatal("invalid source ID accepted")
		}
	})
	t.Run("absent store root", func(t *testing.T) {
		t.Parallel()
		store := NewStore(filepath.Join(t.TempDir(), "missing-store"))
		found, ok, err := store.FindBySource("wbs-source")
		if err != nil || ok || found.ParkedSessionID != "" {
			t.Fatalf("found=%#v ok=%t err=%v", found, ok, err)
		}
	})
	t.Run("unusable store root", func(t *testing.T) {
		t.Parallel()
		if _, _, err := NewStore("relative-store").FindBySource("wbs-source"); err == nil {
			t.Fatal("relative store root accepted")
		}
	})
	t.Run("unexpected artifact", func(t *testing.T) {
		t.Parallel()
		root := spCovStoreRoot(t)
		if err := os.Mkdir(filepath.Join(root, "rogue-artifact"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, _, err := NewStore(root).FindBySource("wbs-source"); err == nil {
			t.Fatal("rogue store artifact accepted")
		}
	})
	t.Run("unreadable aggregate", func(t *testing.T) {
		t.Parallel()
		root := spCovStoreRoot(t)
		dir := filepath.Join(root, "park-broken")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		spCovWriteRaw(t, filepath.Join(dir, sourceBundleFileName), []byte("{}\n"), 0o600)
		if _, _, err := NewStore(root).FindBySource("wbs-source"); err == nil {
			t.Fatal("corrupt aggregate accepted")
		}
	})
	t.Run("sorted lookup finds exact source", func(t *testing.T) {
		t.Parallel()
		store := NewStore(t.TempDir())
		for _, identity := range [][2]string{{"park-zulu", "wbs-zulu"}, {"park-alpha", "wbs-alpha"}} {
			if _, err := store.Create(spCovBundleWithID(t, identity[0], identity[1])); err != nil {
				t.Fatal(err)
			}
		}
		found, ok, err := store.FindBySource("wbs-alpha")
		if err != nil || !ok || found.Source.WBSessionID != "wbs-alpha" {
			t.Fatalf("found=%#v ok=%t err=%v", found, ok, err)
		}
		if _, ok, err := store.FindBySource("wbs-absent"); err != nil || ok {
			t.Fatalf("absent source ok=%t err=%v", ok, err)
		}
	})
}

func TestSpCovLoadRejectsUnusableRootsAndMissingAggregates(t *testing.T) {
	t.Parallel()
	if _, err := NewStore("relative-store").Load("park-test"); err == nil {
		t.Fatal("relative store root accepted")
	}
	if _, err := NewStore(spCovStoreRoot(t)).Load("park-test"); err == nil {
		t.Fatal("missing aggregate accepted")
	}
	if _, err := NewStore(filepath.Join(t.TempDir(), "missing")).Load("park-test"); err == nil {
		t.Fatal("missing store root accepted")
	}
}

func TestSpCovOpenPrivateStoreRootModesAndFailures(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	t.Run("relative root", func(t *testing.T) {
		t.Parallel()
		if _, err := openPrivateStoreRoot("relative", false); err == nil {
			t.Fatal("relative root accepted")
		}
	})
	t.Run("unclean root", func(t *testing.T) {
		t.Parallel()
		if _, err := openPrivateStoreRoot(parent+"/./store", false); err == nil {
			t.Fatal("unclean root accepted")
		}
	})
	t.Run("missing parent without create", func(t *testing.T) {
		t.Parallel()
		if _, err := openPrivateStoreRoot(filepath.Join(parent, "absent", "store"), false); err == nil {
			t.Fatal("missing parent accepted")
		}
	})
	t.Run("missing leaf without create", func(t *testing.T) {
		t.Parallel()
		if _, err := openPrivateStoreRoot(filepath.Join(parent, "absent-store"), false); err == nil {
			t.Fatal("missing leaf accepted")
		}
	})
	t.Run("create beneath a regular file", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(parent, "blocker")
		spCovWriteRaw(t, blocker, []byte("x"), 0o600)
		if _, err := openPrivateStoreRoot(filepath.Join(blocker, "store"), true); err == nil {
			t.Fatal("store created beneath a regular file")
		}
	})
	t.Run("existing root with wrong mode", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(parent, "loose-store")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := openPrivateStoreRoot(root, false); err == nil {
			t.Fatal("0755 root accepted without create")
		}
	})
	t.Run("create tightens and reuses existing root", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(parent, "loose-create-store")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		dir, err := openPrivateStoreRoot(root, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = dir.Close() }()
		info, err := os.Stat(root)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("root info=%v err=%v", info, err)
		}
	})
	t.Run("create fresh root", func(t *testing.T) {
		t.Parallel()
		root := filepath.Join(parent, "fresh-store")
		dir, err := openPrivateStoreRoot(root, true)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = dir.Close() }()
		info, err := os.Stat(root)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("root info=%v err=%v", info, err)
		}
	})
}

func TestSpCovOpenPrivateDirectoryAtModesAndFailures(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "store")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "loose"), 0o755); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parent.Close() }()
	if _, err := openPrivateDirectoryAt(parent, "missing"); err == nil {
		t.Fatal("missing private directory accepted")
	}
	if _, err := openPrivateDirectoryAt(parent, "loose"); err == nil {
		t.Fatal("0755 private directory accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "tight"), 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := openPrivateDirectoryAt(parent, "tight")
	if err != nil {
		t.Fatal(err)
	}
	if err := dir.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSpCovGitRemoteSafetyFixtureIsStable(t *testing.T) {
	t.Parallel()
	// Guard the shared remote fixture so upstream parser changes cannot make
	// every remote test vacuously pass for the wrong reason.
	remote, err := gitremote.Parse("https://github.com/acme/app.git")
	if err != nil || remote.Identity.Repository != "acme/app" {
		t.Fatalf("remote=%#v err=%v", remote, err)
	}
}

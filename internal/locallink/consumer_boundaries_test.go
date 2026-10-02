package locallink

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/streams"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsumerWorkspaceChangesRetainRecoveryEvidence(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"before install", "after applied link"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"2.0.0"}`}, map[string]string{"frontend/package.json": `{"name":"consumer","private":true,"dependencies":{"@acme/core":"1.0.0"}}`, "frontend/pnpm-workspace.yaml": "packages: []\n"})
			identities, err := streams.DiscoverPublished(f.library)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			retained := filepath.Join(f.consumer, "frontend-retained")
			resolve := func(root, relative string) (string, error) {
				calls++
				if (phase == "before install" && calls == 1) || (phase == "after applied link" && calls == 2) {
					if err := os.Rename(filepath.Join(root, filepath.FromSlash(relative)), retained); err != nil {
						t.Fatal(err)
					}
				}
				return workspacePath(root, relative)
			}
			result := f.engine.linkConsumerWithWorkspace(context.Background(), Options{}, Result{LibraryRepository: "acme/library"}, "fixture", f.library, f.consumer, identities, f.git.hash, resolve)
			if len(result.Errors) == 0 || !strings.Contains(strings.Join(result.Errors, " "), "resolve npm workspace") {
				t.Fatalf("changed workspace accepted: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(retained, "package.json")); err != nil {
				t.Fatalf("workspace bytes lost: %v", err)
			}
			if phase == "after applied link" {
				if _, err := os.Stat(linkAppliedMarkerPath(retained, "@acme/core")); err != nil {
					t.Fatalf("applied recovery marker lost: %v", err)
				}
			} else if len(f.node.linked) != 0 {
				t.Fatalf("failed workspace applied links: %v", f.node.linked)
			}
		})
	}
}

func TestGoWorkspaceRenderFailurePreservesConsumerFiles(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	before, err := os.ReadFile(filepath.Join(f.consumer, "backend", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("workspace coordinate unavailable")
	links, err := f.engine.linkGoWithRender(context.Background(), f.library, f.consumer, nil, "acme/library", f.git.hash, func(consumer string, consumerModules []streams.GoModule, library string, libraryModules []streams.GoModule) (string, error) {
		if _, err := renderGoWork(consumer, consumerModules, library, libraryModules); err != nil {
			t.Fatal(err)
		}
		return "", failure
	})
	if links != nil || !errors.Is(err, failure) {
		t.Fatalf("render failure=%v %v", links, err)
	}
	after, err := os.ReadFile(filepath.Join(f.consumer, "backend", "go.mod"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("module changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.consumer, goWorkFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace published: %v", err)
	}
}

type recordingFailureNode struct {
	*fakeNode
	afterLink func()
}

func (node *recordingFailureNode) Link(ctx context.Context, consumer, packageName, dist string) (NodeLinkResult, error) {
	result, err := node.fakeNode.Link(ctx, consumer, packageName, dist)
	if err == nil {
		node.afterLink()
	}
	return result, err
}

func TestAppliedLinkRecordFailureKeepsRecoverableFilesystemEvidence(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"2.0.0"}`}, map[string]string{"package.json": `{"dependencies":{"@acme/core":"1.0.0"}}`})
	path := filepath.Join(f.store.Root, "fixture", "stream.json")
	f.engine.Node = &recordingFailureNode{fakeNode: f.node, afterLink: func() {
		if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := f.engine.Run(context.Background(), Options{Library: f.library, Consumers: []string{f.consumer}, Stream: "fixture"})
	if err != nil || len(result.Consumers) != 1 || !strings.Contains(strings.Join(result.Consumers[0].Errors, " "), "record links in stream fixture") {
		t.Fatalf("record failure=%+v %v", result, err)
	}
	if _, err := os.Stat(linkAppliedMarkerPath(f.consumer, "@acme/core")); err != nil {
		t.Fatalf("recovery marker removed: %v", err)
	}
	if len(result.Consumers[0].Links) != 1 {
		t.Fatalf("applied evidence lost: %+v", result.Consumers[0])
	}
}

func TestUnreadableStreamStoreRefusesLinkBeforeFilesystemMutation(t *testing.T) {
	t.Parallel()
	f := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	blocked := filepath.Join(t.TempDir(), "streams")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := f.engine.linkWithConsumerResolution(context.Background(), Options{Library: f.library, Consumers: []string{f.consumer}}, filepath.Abs, func(options Options) (map[string]string, []string, error) {
		f.engine.Store = streams.OpenAt(blocked)
		return f.engine.resolveConsumerStreams(options)
	})
	if err == nil || !strings.Contains(err.Error(), "read stream store") || len(result.Consumers) != 0 {
		t.Fatalf("store failure=%+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(f.consumer, goWorkFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace mutation despite unreadable state: %v", err)
	}
}

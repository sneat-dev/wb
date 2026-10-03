package sessionrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmessage"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type operationContextKey struct{}

func TestListDetachesAttributionAndPreservesWarningsAndLiveFiltering(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	want := errors.New("effect")
	base := func() ListDependencies {
		return ListDependencies{
			Directory: func(string) (string, error) { return "records", nil }, Records: func(string) ([]session.View, error) {
				return []session.View{{Record: session.Record{WBSessionID: "live"}, State: session.StateLive}, {Record: session.Record{WBSessionID: "gone"}, State: session.StateGone}}, nil
			},
			Worktrees: func(got context.Context, _ worktrees.ListOptions) ([]worktrees.ListResult, error) {
				if got.Err() != nil {
					t.Fatal("attribution inherited cancellation")
				}
				return nil, nil
			},
			Home: func(string) (string, error) { return "private", nil }, Waits: func(string, waitregistry.Options) ([]waitregistry.Record, error) {
				return []waitregistry.Record{{WBSessionID: "live", Kind: "checks", Targets: []string{"a", "b"}, Stale: true}}, nil
			},
		}
	}
	deps := base()
	rows, err := NewList(deps).List(ctx, ListRequest{OnlyLive: true}, nil)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Waiting, []string{"checks a,b (stale)"}) {
		t.Fatalf("rows=%+v error=%v", rows, err)
	}
	deps.Records = func(string) ([]session.View, error) { return []session.View{{State: session.StateGone}}, nil }
	rows, err = NewList(deps).List(ctx, ListRequest{OnlyLive: true}, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%+v error=%v", rows, err)
	}
	for _, stage := range []string{"directory", "records", "home", "waits"} {
		t.Run(stage, func(t *testing.T) {
			deps := base()
			warned := []string{}
			switch stage {
			case "directory":
				deps.Directory = func(string) (string, error) { return "", want }
			case "records":
				deps.Records = func(string) ([]session.View, error) { return nil, want }
			case "home":
				deps.Home = func(string) (string, error) { return "", want }
			case "waits":
				deps.Waits = func(string, waitregistry.Options) ([]waitregistry.Record, error) { return nil, want }
			}
			_, err := NewList(deps).List(ctx, ListRequest{}, func(s string) { warned = append(warned, s) })
			if stage == "directory" || stage == "records" {
				if !errors.Is(err, want) {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if stage == "waits" && (len(warned) != 1 || !strings.Contains(warned[0], "outstanding waits")) {
				t.Fatalf("warnings=%v", warned)
			}
			if stage == "waits" {
				if _, err := NewList(deps).List(ctx, ListRequest{}, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRegisterAndPrunePropagateDirectoryErrorsWithoutLaterEffects(t *testing.T) {
	t.Parallel()
	want := errors.New("directory")
	deps := DefaultRegisterDependencies()
	deps.CurrentPID = func() int { return -1 }
	deps.ParentPID = func() int { return -2 }
	deps.Directory = func(string) (string, error) { return "", want }
	deps.Register = func(string, session.Record) (session.Record, error) {
		t.Fatal("register reached")
		return session.Record{}, nil
	}
	if _, err := NewRegister(deps).Register(context.Background(), RegisterRequest{Record: session.Record{PID: 41}}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	prune := PruneDependencies{Directory: func(string) (string, error) { return "", want }, Prune: func(string) (int, error) { t.Fatal("prune reached"); return 0, nil }}
	if _, err := NewPrune(prune).Prune(context.Background(), "private"); !errors.Is(err, want) {
		t.Fatal(err)
	}
}

func TestReceiveOperationsBindContextAndPropagateIdentityAndStoreErrors(t *testing.T) {
	t.Parallel()
	want := errors.New("identity/store")
	ctx := context.WithValue(context.Background(), operationContextKey{}, "private")
	for _, stage := range []string{"identity", "store", "success"} {
		t.Run(stage, func(t *testing.T) {
			identity := func() (string, error) {
				if stage == "identity" {
					return "", want
				}
				return "machine", nil
			}
			store := func(root string) (sessionmove.Store, error) {
				if root != "private" {
					t.Fatal(root)
				}
				if stage == "store" {
					return sessionmove.Store{}, want
				}
				return sessionmove.Store{}, nil
			}
			receive := ReceiveDependencies{LocalMachine: identity, Store: store, Receive: func(got context.Context, r sessionreceive.Options) (sessionreceive.Result, error) {
				if got != ctx || r.LocalMachine != "machine" || string(r.RawRequest) != "exact" {
					t.Fatalf("binding=%+v", r)
				}
				return sessionreceive.Result{}, nil
			}}
			park := ReceiveParkDependencies{LocalMachine: identity, Store: func(root string) (sessionpark.TargetStore, error) {
				_, err := store(root)
				return sessionpark.TargetStore{}, err
			}, Receive: func(got context.Context, r sessionparkreceive.Options) (sessionparkreceive.Result, error) {
				if got != ctx {
					t.Fatal("context")
				}
				return sessionparkreceive.Result{}, nil
			}}
			message := ReceiveMessageDependencies{LocalMachine: identity, Store: store, Directory: func(string) (string, error) { return "records", nil }, Receive: func(got context.Context, r sessionmessage.Options) (sessionmessage.Result, error) {
				if got != ctx {
					t.Fatal("context")
				}
				return sessionmessage.Result{}, nil
			}}
			request := ReceiveRequest{ProjectsRoot: "private", Raw: []byte("exact")}
			_, a := NewReceive(receive).Receive(ctx, request)
			_, b := NewReceivePark(park).ReceivePark(ctx, request)
			_, c := NewReceiveMessage(message).ReceiveMessage(ctx, request)
			for _, err := range []error{a, b, c} {
				if stage == "success" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, want) {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func privateScanner() (*secretscan.Scanner, []string, error) {
	empty := ""
	return secretscan.LoadDefault(secretscan.LoadOptions{EnvExtraRulesPath: &empty})
}
func TestParkRefusalsAndEffectsPreserveOrderWithoutLaterWrites(t *testing.T) {
	t.Parallel()
	want := errors.New("park effect")
	for _, stage := range []string{"empty", "oversize", "override", "scanner", "directory", "source", "list", "home", "store", "id", "capture", "create-conflict", "success"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			events := []string{}
			hit := func(s string) error {
				events = append(events, s)
				if stage == s {
					return want
				}
				return nil
			}
			source := session.Record{PID: 41, WBSessionID: "source", Machine: "source", Runtime: "codex", StartedAt: time.Unix(10, 0).UTC()}
			deps := ParkDependencies{
				Directory: func(string) (string, error) { return root, hit("directory") }, Home: func(string) (string, error) { return root, hit("home") }, PID: func() int { return 41 },
				ResolveSource: func(string, int, session.AutoRegisterHints) (session.Record, bool, error) {
					return source, false, hit("source")
				},
				List: func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
					return []worktrees.ListResult{{WorkLogSessionID: "source"}}, hit("list")
				},
				Store: func(path string) sessionpark.Store {
					if stage == "store" {
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return sessionpark.NewStore(path)
				},
				Capture: func(_ context.Context, _ string, _ []worktrees.ListResult, _ session.Record, persist func([]sessionpark.Worktree) error) error {
					if err := hit("capture"); err != nil {
						return err
					}
					if stage == "create-conflict" {
						other := source
						other.WBSessionID = "other-source"
						if _, err := sessionpark.NewStore(filepath.Join(root, "parked-sessions")).Create(sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-private", Source: other, Continuation: "other", ParkedAt: time.Unix(11, 0).UTC()}); err != nil {
							t.Fatal(err)
						}
					}
					return persist(nil)
				},
				NewID: func() (string, error) { return "park-private", hit("id") }, Now: func() time.Time { return time.Unix(11, 0).UTC() }, MarkParked: func(string, int, string) (session.Record, error) { return source, nil },
				LoadScanner: func() (*secretscan.Scanner, []string, error) {
					if err := hit("scanner"); err != nil {
						return nil, nil, err
					}
					return privateScanner()
				},
			}
			request := ParkRequest{ProjectsRoot: root, Continuation: []byte("private continuation")}
			switch stage {
			case "empty":
				request.Continuation = nil
			case "oversize":
				request.Continuation = []byte(strings.Repeat("x", sessionpark.MaxContinuationBytes+1))
			case "override":
				request.OverrideSecrets = []string{"invalid"}
			}
			result, err := NewPark(deps).Park(context.Background(), request)
			switch stage {
			case "success":
				if err != nil || result.Output.ParkedSessionID != "park-private" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				before := len(events)
				retry, retryErr := NewPark(deps).Park(context.Background(), request)
				if retryErr != nil || retry.Output != result.Output {
					t.Fatalf("retry=%+v error=%v", retry, retryErr)
				}
				for _, event := range events[before:] {
					if event == "id" {
						t.Fatal("immutable retry allocated another identifier")
					}
				}
			case "empty", "oversize", "override", "store", "create-conflict":
				if err == nil {
					t.Fatal("refusal accepted")
				}
			default:
				if !errors.Is(err, want) {
					t.Fatalf("events=%v error=%v", events, err)
				}
			}
			if stage == "empty" || stage == "oversize" || stage == "override" {
				if len(events) != 0 {
					t.Fatalf("later effect=%v", events)
				}
			}
		})
	}
}

func TestResumeUsesPrivateHomeAndStopsOnAcquireOrLoadFailure(t *testing.T) {
	t.Parallel()
	want := errors.New("home")
	if _, err := NewResume(ResumeDependencies{Home: func(string) (string, error) { return "", want }}).Resume(context.Background(), ResumeRequest{ParkedSessionID: "p"}); !errors.Is(err, want) {
		t.Fatal(err)
	}
	home := t.TempDir()
	deps := ResumeDependencies{Home: func(string) (string, error) { return home, nil }}
	if _, err := NewResume(deps).Resume(context.Background(), ResumeRequest{ParkedSessionID: "../escape"}); err == nil {
		t.Fatal("invalid identifier accepted")
	}
	if _, err := NewResume(deps).Resume(context.Background(), ResumeRequest{ParkedSessionID: "missing"}); err == nil {
		t.Fatal("missing bundle accepted")
	}
}

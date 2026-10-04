package cmdpeers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
	"github.com/spf13/cobra"
)

var errBoundaryFailure = errors.New("boundary refused")

func boundaryDependencies() Dependencies {
	return Dependencies{
		Invite: func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
			return peersrun.InviteResult{Output: peersrun.InviteOutput{PeerID: "id", Name: "laptop", Token: "secret"}}, nil
		},
		Join: func(context.Context, peersrun.JoinRequest, io.Reader, func(error)) (peersrun.JoinOutput, error) {
			return peersrun.JoinOutput{HubURL: "https://hub.test", ConfigPath: "config", TokenFile: "credential", Verified: true}, nil
		},
		List: func(context.Context, peersrun.ListRequest, func(error)) (peersrun.ListResult, error) {
			return peersrun.ListResult{Response: peers.ListResponse{SchemaVersion: 1, Peers: []peers.Record{{Name: "laptop"}}}}, nil
		},
		Get: func(context.Context, peersrun.GetRequest) (peers.Detail, error) {
			return peers.Detail{Record: peers.Record{Name: "laptop"}}, nil
		},
		TrustChange: func(context.Context, peersrun.TrustRequest) (peersrun.TrustResult, error) {
			return peersrun.TrustResult{Response: peers.TrustResponse{Name: "laptop", Trust: "active"}}, nil
		},
		Disconnect: func(context.Context, peersrun.DisconnectRequest) (peers.DisconnectResponse, error) {
			return peers.DisconnectResponse{Name: "laptop", Message: "closed"}, nil
		}, Now: time.Now, SetDiscoveryTerms: func(*cobra.Command, string) {},
	}
}
func boundaryCommand(deps Dependencies) *cobra.Command {
	return New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects"} }, ExitError: func(code int, message string) error { return &fixtureExitError{code: code, message: message} }}, deps)
}
func executeBoundary(command *cobra.Command, args []string, out, errOut io.Writer) error {
	command.SetArgs(args)
	command.SetOut(out)
	command.SetErr(errOut)
	command.SilenceErrors = true
	command.SilenceUsage = true
	return command.Execute()
}
func TestAllPeerVerbsRenderTextAndJSON(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"invite", "join", "list", "get", "block", "unblock", "disconnect"} {
		for _, format := range []string{"text", "json"} {
			t.Run(verb+format, func(t *testing.T) {
				t.Parallel()
				deps := boundaryDependencies()
				args := []string{verb}
				if verb != "list" {
					args = append(args, "laptop")
				}
				args = append(args, "--format="+format)
				var out bytes.Buffer
				if err := executeBoundary(boundaryCommand(deps), args, &out, io.Discard); err != nil {
					t.Fatal(err)
				}
				if out.Len() == 0 {
					t.Fatal("no output")
				}
				if format == "json" && !strings.HasPrefix(out.String(), "{") {
					t.Fatalf("not JSON:%q", out.String())
				}
			})
		}
	}
}
func TestAllPeerOperationsReturnTheirOriginalError(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"invite", "join", "list", "get", "block", "unblock", "disconnect"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := boundaryDependencies()
			deps.Invite = func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
				return peersrun.InviteResult{}, errBoundaryFailure
			}
			deps.Join = func(context.Context, peersrun.JoinRequest, io.Reader, func(error)) (peersrun.JoinOutput, error) {
				return peersrun.JoinOutput{}, errBoundaryFailure
			}
			deps.List = func(context.Context, peersrun.ListRequest, func(error)) (peersrun.ListResult, error) {
				return peersrun.ListResult{}, errBoundaryFailure
			}
			deps.Get = func(context.Context, peersrun.GetRequest) (peers.Detail, error) {
				return peers.Detail{}, errBoundaryFailure
			}
			deps.TrustChange = func(context.Context, peersrun.TrustRequest) (peersrun.TrustResult, error) {
				return peersrun.TrustResult{}, errBoundaryFailure
			}
			deps.Disconnect = func(context.Context, peersrun.DisconnectRequest) (peers.DisconnectResponse, error) {
				return peers.DisconnectResponse{}, errBoundaryFailure
			}
			args := []string{verb}
			if verb != "list" {
				args = append(args, "laptop")
			}
			var out bytes.Buffer
			err := executeBoundary(boundaryCommand(deps), args, &out, io.Discard)
			if !errors.Is(err, errBoundaryFailure) || out.Len() != 0 {
				t.Fatalf("err=%v out=%q", err, out.String())
			}
		})
	}
}
func TestPeerWritersAndTypedFindings(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"invite", "join", "list", "get", "block", "unblock", "disconnect"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := boundaryDependencies()
			args := []string{verb}
			if verb != "list" {
				args = append(args, "laptop")
			}
			args = append(args, "--json")
			err := executeBoundary(boundaryCommand(deps), args, &failAtCallWriter{failAt: 1}, io.Discard)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	deps := boundaryDependencies()
	deps.List = func(_ context.Context, _ peersrun.ListRequest, warn func(error)) (peersrun.ListResult, error) {
		warn(errBoundaryFailure)
		return peersrun.ListResult{Response: peers.ListResponse{SchemaVersion: 1}, Finding: &peersrun.Refusal{Kind: peersrun.Findings, Message: "finding"}}, nil
	}
	var out, diagnostic bytes.Buffer
	err := executeBoundary(boundaryCommand(deps), []string{"list", "--json"}, &out, &diagnostic)
	var coded *fixtureExitError
	if !errors.As(err, &coded) || coded.code != shared.ExitFindings || !strings.Contains(diagnostic.String(), "wb: downstream peers unavailable: boundary refused") || out.Len() == 0 {
		t.Fatalf("err=%v out=%q diagnostic=%q", err, out.String(), diagnostic.String())
	}
}
func TestInviteRescueKeepsAttemptedAbsolutePathOutOfJSON(t *testing.T) {
	t.Parallel()
	deps := boundaryDependencies()
	deps.Invite = func(_ context.Context, req peersrun.InviteRequest) (peersrun.InviteResult, error) {
		if req.TokenFile != "relative.token" {
			t.Fatal(req)
		}
		return peersrun.InviteResult{Output: peersrun.InviteOutput{Name: "laptop", Token: "secret"}, AttemptedTokenFile: "/resolved/relative.token", TokenWriteError: errBoundaryFailure}, nil
	}
	for _, jsonOut := range []bool{false, true} {
		args := []string{"invite", "laptop", "--token-file=relative.token"}
		if jsonOut {
			args = append(args, "--json")
		}
		var out bytes.Buffer
		err := executeBoundary(boundaryCommand(deps), args, &out, io.Discard)
		var coded *fixtureExitError
		if !errors.As(err, &coded) || coded.code != shared.ExitFindings || !strings.Contains(err.Error(), "/resolved/relative.token") {
			t.Fatalf("err=%v", err)
		}
		if jsonOut {
			if strings.Contains(out.String(), "token_file") || strings.Contains(out.String(), "resolved") || !strings.Contains(out.String(), `"token":"secret"`) {
				t.Fatalf("json=%q", out.String())
			}
		} else if !strings.Contains(out.String(), "could not write token file /resolved/relative.token") || !strings.Contains(out.String(), "secret") {
			t.Fatal(out.String())
		}
		err = executeBoundary(boundaryCommand(deps), args, &failAtCallWriter{failAt: 1}, io.Discard)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("writer err=%v", err)
		}
	}
}
func TestInviteSuccessFilesRotationAndWriterRefusals(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"", "/resolved/token"} {
		for _, failureAt := range []int{0, 1, 2} {
			deps := boundaryDependencies()
			deps.Invite = func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
				return peersrun.InviteResult{Output: peersrun.InviteOutput{PeerID: "id", Name: "laptop", Token: "secret", TokenFile: path, Rotated: true}}, nil
			}
			writer := &failAtCallWriter{failAt: failureAt}
			err := executeBoundary(boundaryCommand(deps), []string{"invite", "laptop"}, writer, io.Discard)
			if (failureAt != 0) != errors.Is(err, errAtWrite) {
				t.Fatalf("path=%q failAt=%d err=%v", path, failureAt, err)
			}
		}
	}
}
func TestJoinWarningAndUpstreamTrustRenderAtOriginalBoundaries(t *testing.T) {
	t.Parallel()
	deps := boundaryDependencies()
	deps.Join = func(_ context.Context, _ peersrun.JoinRequest, _ io.Reader, warn func(error)) (peersrun.JoinOutput, error) {
		warn(errBoundaryFailure)
		return peersrun.JoinOutput{HubURL: "https://hub.test", DaemonRestart: true}, nil
	}
	var out, diag bytes.Buffer
	if err := executeBoundary(boundaryCommand(deps), []string{"join", "hub"}, &out, &diag); err != nil || !strings.Contains(out.String(), "restarted if running") || !strings.Contains(diag.String(), "wb: node identity unavailable:") {
		t.Fatalf("%v %q %q", err, out.String(), diag.String())
	}
	deps.TrustChange = func(context.Context, peersrun.TrustRequest) (peersrun.TrustResult, error) {
		return peersrun.TrustResult{Response: peers.TrustResponse{Name: "hub.test"}, Upstream: true}, nil
	}
	out.Reset()
	if err := executeBoundary(boundaryCommand(deps), []string{"block", "upstream"}, &out, io.Discard); err != nil || out.String() != "Blocked upstream hub.test\n" {
		t.Fatalf("%v %q", err, out.String())
	}
	if err := executeBoundary(boundaryCommand(deps), []string{"block", "upstream"}, &failAtCallWriter{failAt: 1}, io.Discard); !errors.Is(err, errAtWrite) {
		t.Fatal(err)
	}
}
func TestParsedRuntimeFlagsAndContextAreInvocationLocal(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"first", "second"} {
		root := "before"
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		deps := boundaryDependencies()
		deps.Invite = func(got context.Context, req peersrun.InviteRequest) (peersrun.InviteResult, error) {
			if got != ctx || req.Root != value || req.Name != "laptop" || !req.Rotate {
				t.Fatalf("ctx=%v req=%+v", got, req)
			}
			return peersrun.InviteResult{}, nil
		}
		command := New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }, ExitError: func(code int, message string) error { return &fixtureExitError{code: code, message: message} }}, deps)
		command.PersistentFlags().StringVar(&root, "projects-root", root, "root")
		command.SetContext(ctx)
		if err := executeBoundary(command, []string{"invite", "laptop", "--rotate", "--projects-root=" + value}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
}
func TestArgumentAndFormatErrorsCallNoOperation(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"invite"}, {"get"}, {"join"}, {"block"}, {"unblock"}, {"disconnect"}, {"list", "extra"}, {"invite", "laptop", "--format=yaml"}} {
		deps := boundaryDependencies()
		deps.Invite = func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
			t.Fatal("unexpected operation")
			return peersrun.InviteResult{}, nil
		}
		if err := executeBoundary(boundaryCommand(deps), args, io.Discard, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestUpstreamTrustJSONUsesTheCommandRenderer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		verb, want string
		action     peersrun.TrustAction
	}{{"block", "blocked", peersrun.Block}, {"unblock", "active", peersrun.Unblock}} {
		t.Run(tc.verb, func(t *testing.T) {
			t.Parallel()
			deps := boundaryDependencies()
			deps.TrustChange = func(_ context.Context, request peersrun.TrustRequest) (peersrun.TrustResult, error) {
				if request.Peer != "upstream" || request.Action != tc.action {
					t.Fatalf("request=%+v", request)
				}
				return peersrun.TrustResult{Response: peers.TrustResponse{PeerID: "hub.example.test", Name: "hub.example.test", Trust: tc.want}, Upstream: true}, nil
			}
			var out bytes.Buffer
			if err := executeBoundary(boundaryCommand(deps), []string{tc.verb, "upstream", "--json"}, &out, io.Discard); err != nil {
				t.Fatal(err)
			}
			var response peers.TrustResponse
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.PeerID != "hub.example.test" || response.Trust != tc.want {
				t.Fatalf("%s response=%+v", tc.verb, response)
			}
		})
	}
}

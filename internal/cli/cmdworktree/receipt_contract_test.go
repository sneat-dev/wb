package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/graduation"
	"testing"
)

type receiptFailWriter struct{ err error }
type receiptContextKey struct{}

func (writer receiptFailWriter) Write([]byte) (int, error) { return 0, writer.err }
func TestReceiptAdapterPassesEveryPathAndPreservesOperationAndWriterErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("receipt operation failed")
	for _, remote := range []bool{false, true} {
		for _, operationFailure := range []bool{false, true} {
			t.Run(map[bool]string{false: "compose", true: "observe"}[remote]+map[bool]string{false: " writer", true: " operation"}[operationFailure], func(t *testing.T) {
				t.Parallel()
				var gotPaths graduation.EvidencePaths
				var gotRequest graduation.RemoteTargetRequest
				ctx := context.WithValue(context.Background(), receiptContextKey{}, "receipt-context")
				deps := ReceiptDependencies{Compose: func(paths graduation.EvidencePaths) ([]byte, error) {
					gotPaths = paths
					if operationFailure {
						return nil, boom
					}
					return []byte("receipt\n"), nil
				}, Observe: func(received context.Context, request graduation.RemoteTargetRequest) ([]byte, error) {
					if received != ctx {
						t.Error("lost command context")
					}
					gotRequest = request
					if operationFailure {
						return nil, boom
					}
					return []byte("remote\n"), nil
				}}
				command := NewReceipt(deps)
				command.SetContext(ctx)
				command.SilenceErrors = true
				command.SetOut(receiptFailWriter{boom})
				command.SetErr(&bytes.Buffer{})
				args := []string{"--local-check", "local", "--ci-wait", "ci", "--remote-target", "remote", "--deployed-revision", "deploy", "--terminal-cleanup", "cleanup", "--output", "receipt.json"}
				if remote {
					args = []string{"remote-target", "--repo", "owner/repo", "--repository-path", "checkout", "--remote", "upstream", "--target", "main", "--output", "remote.json"}
				}
				command.SetArgs(args)
				if err := command.Execute(); !errors.Is(err, boom) {
					t.Fatalf("error=%v", err)
				}
				if remote {
					if gotRequest != (graduation.RemoteTargetRequest{Repository: "owner/repo", RepositoryPath: "checkout", Remote: "upstream", Target: "main", Output: "remote.json"}) {
						t.Fatalf("request=%+v", gotRequest)
					}
				} else if gotPaths != (graduation.EvidencePaths{LocalCheck: "local", CIWait: "ci", RemoteTarget: "remote", DeployedRevision: "deploy", TerminalCleanup: "cleanup", Output: "receipt.json"}) {
					t.Fatalf("paths=%+v", gotPaths)
				}
			})
		}
	}
}

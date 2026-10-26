package landinglane

import (
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestLegacyLaneObservationUsesRunnerAndFailsClosed(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"idle", "active", "observation denied"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			name := "other"
			if mode != "idle" {
				name = "wb-operation"
			}
			var commandErr error
			denied := errors.New("ps refused")
			if mode == "observation denied" {
				commandErr = denied
			}
			fake.Expect(func(call runnertest.Call) bool {
				if call.Name != "ps" || call.Dir != "" || !reflect.DeepEqual(call.Args, []string{"-A", "-o", "pid=", "-o", "comm="}) {
					return false
				}
				if call.Opts.Env != nil || call.Opts.CaptureCombined {
					t.Fatal("process-table environment or output separation changed")
				}
				return true
			}, runner.Result{Stdout: fmt.Sprintf("%d %s\n", os.Getpid()+1, name), Stderr: "permission denied\n"}, commandErr)
			active, err := legacyOperationActiveWithRunner(fake)
			if mode == "observation denied" {
				if active || !errors.Is(err, denied) || !strings.Contains(err.Error(), "permission denied") {
					t.Fatalf("observation did not fail closed: active=%t err=%v", active, err)
				}
			} else if err != nil || active != (mode == "active") {
				t.Fatalf("active=%t err=%v", active, err)
			}
		})
	}
}

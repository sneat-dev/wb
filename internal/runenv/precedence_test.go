package runenv

import (
	"github.com/sneat-dev/wb/internal/runqueue"
	"reflect"
	"testing"
)

func TestCallerSpecificZeroUnitAndInheritedPrecedence(t *testing.T) {
	t.Parallel()
	base := []string{"PATH=/bin", "GOMAXPROCS=3", "GOFLAGS=-mod=vendor", "WB_CPU_UNITS=old", "NX_PARALLEL=old", "WB_OPERATION_ID=old"}
	original := append([]string(nil), base...)
	additions := map[string]string{"GOMAXPROCS": "99", "GOFLAGS": "-tags=durable", "WB_CPU_UNITS": "99", "NX_PARALLEL": "99", "WB_OPERATION_ID": "durable", "EXTRA": "kept"}
	for _, units := range []int{0, -1} {
		sync := Synchronous(base, []string{"pytest"}, "new", units, "-tags=resolved")
		if runqueue.LookupEnv(sync, "WB_OPERATION_ID") != "new" || runqueue.LookupEnv(sync, "WB_CPU_UNITS") != "old" || runqueue.LookupEnv(sync, "NX_PARALLEL") != "old" || runqueue.LookupEnv(sync, "GOFLAGS") != "-mod=vendor" {
			t.Fatalf("synchronous units%d=%v", units, sync)
		}
		worker := Worker(base, []string{"pytest"}, "new", units, "-tags=resolved")
		if runqueue.LookupEnv(worker, "WB_CPU_UNITS") == "old" || runqueue.LookupEnv(worker, "GOFLAGS") != "-tags=resolved" {
			t.Fatalf("worker units%d=%v", units, worker)
		}
	}
	daemon := Daemon(base, []string{"pytest"}, additions, "new", 4, "-tags=resolved")
	for key, want := range map[string]string{"GOMAXPROCS": "3", "GOFLAGS": "-tags=resolved", "WB_CPU_UNITS": "4", "NX_PARALLEL": "4", "WB_OPERATION_ID": "new", "EXTRA": "kept", "PATH": "/bin"} {
		if got := runqueue.LookupEnv(daemon, key); got != want {
			t.Errorf("%s=%s want%s", key, got, want)
		}
	}
	preserved := Daemon(base, []string{"pytest"}, additions, "new", 4, "")
	if runqueue.LookupEnv(preserved, "GOFLAGS") != "-tags=durable" {
		t.Fatal(preserved)
	}
	sync := Synchronous(base, []string{"pytest"}, "new", 4, "-tags=resolved")
	if runqueue.LookupEnv(sync, "GOFLAGS") != "-tags=resolved" {
		t.Fatal(sync)
	}
	if !reflect.DeepEqual(base, original) || additions["GOMAXPROCS"] != "99" || additions["WB_OPERATION_ID"] != "durable" {
		t.Fatal("input mutation")
	}
	daemon[0] = "changed"
	if base[0] != "PATH=/bin" {
		t.Fatal("output aliases base")
	}
}

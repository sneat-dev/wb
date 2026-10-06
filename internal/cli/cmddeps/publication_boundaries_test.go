package cmddeps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/npmrelease"
)

func TestPublicationCommandReadsCurrentFlagsWritersAndContextOnEveryExecute(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before", NonInteractive: true}
	runtime := testRuntime()
	runtime.Flags = func() shared.Flags { return flags }
	sentinel := errors.New("operation refusal")
	var out, notes bytes.Buffer
	calls := 0
	ops := PublicationDependencies{Campaign: func(w io.Writer, enabled bool, label string) *cliprogress.Campaign {
		if w != &notes || enabled {
			t.Fatal("progress writer/terminal policy changed")
		}
		return cliprogress.NewCampaign(w, enabled, label)
	}, Run: func(ctx context.Context, r depsrun.PublicationRequest, cb depsrun.PublicationCallbacks) error {
		calls++
		if ctx != t.Context() || r.Selection.ProjectsRoot != flags.ProjectsRoot || r.Lifecycle.GitHubDir != flags.ProjectsRoot || r.Lifecycle.Checks != nil || r.Checks != "lint" || !r.Lifecycle.ParallelExplicit {
			t.Fatalf("current request=%+v", r)
		}
		if err := cb.ValidateOutput(); err != nil {
			return err
		}
		progress := cb.StartProgress("selection")
		progress.Finish("selected")
		if err := cb.Emit(depsrun.PublicationOutput{Publication: npmrelease.Report{Operation: flags.ProjectsRoot}}); err != nil {
			return err
		}
		return sentinel
	}}
	family := NewPublish(runtime, ops)
	family.SetOut(&out)
	family.SetErr(&notes)
	family.SetContext(t.Context())
	family.SilenceErrors = true
	family.SilenceUsage = true
	for _, root := range []string{"first", "second"} {
		flags.ProjectsRoot = root
		family.SetArgs([]string{"npm", "--checks", "lint", "--parallel", "2", "--format", "json"})
		if err := family.Execute(); !errors.Is(err, sentinel) {
			t.Fatalf("operation identity=%v", err)
		}
		if !strings.Contains(out.String(), root) {
			t.Fatalf("current output=%q", out.String())
		}
		out.Reset()
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	family.SetArgs([]string{"npm", "extra"})
	if err := family.Execute(); err == nil || calls != 2 {
		t.Fatal("arguments reached operation")
	}
}

func TestPublicationRenderUsesActualClosedReportsAndWriterErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("writer refusal")
	for _, format := range []string{"json", "yaml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			if err := writeNpmPublishOutput(publicationFailWriter{sentinel}, depsrun.PublicationOutput{Publication: npmrelease.Report{Operation: "private"}}, format); !errors.Is(err, sentinel) {
				t.Fatalf("writer error=%v", err)
			}
		})
	}
	if err := writeNpmPublishOutput(io.Discard, depsrun.PublicationOutput{}, "toml"); err == nil {
		t.Fatal("unknown format accepted")
	}
}

type publicationFailWriter struct{ err error }

func (w publicationFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestPublicationOperationsBindActualServiceAndCampaign(t *testing.T) {
	t.Parallel()
	runtime := testRuntime()
	service := depsrun.NewPublication(depsrun.DefaultPublicationDependencies(io.Discard), runtime.ExitError)
	ops := PublicationOperations(service)
	if err := ops.Run(t.Context(), depsrun.PublicationRequest{}, depsrun.PublicationCallbacks{}); err == nil {
		t.Fatal("actual identity preflight not reached")
	}
	campaign := ops.Campaign(io.Discard, false, "native default binding")
	campaign.Finish("done")
}

func TestPublicationCompositeSerializationHasAClosedSupportedTypeTree(t *testing.T) {
	t.Parallel()
	seen := map[reflect.Type]bool{}
	active := map[reflect.Type]bool{}
	var inspect func(reflect.Type)
	inspect = func(typ reflect.Type) {
		if typ == reflect.TypeFor[time.Time]() {
			return
		}
		if active[typ] {
			t.Fatalf("recursive report type %s invalidates serialization proof", typ)
		}
		if seen[typ] {
			return
		}
		active[typ] = true
		defer func() { delete(active, typ); seen[typ] = true }()
		for _, candidate := range []reflect.Type{typ, reflect.PointerTo(typ)} {
			for _, name := range []string{"MarshalYAML", "MarshalJSON", "MarshalText"} {
				if _, ok := candidate.MethodByName(name); ok {
					t.Fatalf("new custom marshaler %s.%s invalidates closed report invariant", typ, name)
				}
			}
		}
		switch typ.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				if field.IsExported() {
					inspect(field.Type)
				}
			}
		case reflect.Pointer, reflect.Slice, reflect.Array:
			inspect(typ.Elem())
		case reflect.Map:
			if typ.Key().Kind() != reflect.String {
				t.Fatalf("unsupported map key %s", typ)
			}
			inspect(typ.Key())
			inspect(typ.Elem())
		default:
			t.Fatalf("unsupported report type %s", typ)
		}
	}
	inspect(reflect.TypeFor[depsrun.PublicationOutput]())
	out := depsrun.PublicationOutput{Publication: npmrelease.Report{Releases: []npmrelease.Receipt{{DispatchAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}}}
	for _, format := range []string{"json", "yaml"} {
		var buffer bytes.Buffer
		if err := writeNpmPublishOutput(&buffer, out, format); err != nil {
			t.Fatalf("actual normalized %s date error=%v", format, err)
		}
		if !strings.Contains(buffer.String(), "10000") {
			t.Fatalf("date lost: %s", buffer.String())
		}
	}
}

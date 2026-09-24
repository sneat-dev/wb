package daemonv1

import (
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// dapbLegacyMessage is the deprecated protobuf v1 message surface that every
// generated message still implements (v2's proto.Message only exposes
// ProtoReflect).
type dapbLegacyMessage interface {
	Reset()
	String() string
	ProtoMessage()
}

// dapbMsgCase describes one generated message type so the table-driven tests
// below can exercise every message without repeating the same assertions.
type dapbMsgCase struct {
	name string
	// fresh returns a newly allocated, zero-valued message of this type.
	fresh func() proto.Message
	// desc calls the deprecated generated Descriptor method on a nil receiver.
	desc func() ([]byte, []int)
}

// dapbMessageCases lists every message generated into daemon.pb.go (19 named
// messages plus the nested SubmitOperationRequest.EnvironmentEntry map entry,
// which has no Go type of its own). descIndex is the index baked into the
// generated Descriptor method.
func dapbMessageCases() []dapbMsgCase {
	return []dapbMsgCase{
		{"GetDaemonInfoRequest", func() proto.Message { return &GetDaemonInfoRequest{} }, func() ([]byte, []int) { return (*GetDaemonInfoRequest)(nil).Descriptor() }},
		{"GetDaemonInfoResponse", func() proto.Message { return &GetDaemonInfoResponse{} }, func() ([]byte, []int) { return (*GetDaemonInfoResponse)(nil).Descriptor() }},
		{"SubmitOperationRequest", func() proto.Message { return &SubmitOperationRequest{} }, func() ([]byte, []int) { return (*SubmitOperationRequest)(nil).Descriptor() }},
		{"GetOperationRequest", func() proto.Message { return &GetOperationRequest{} }, func() ([]byte, []int) { return (*GetOperationRequest)(nil).Descriptor() }},
		{"WaitOperationRequest", func() proto.Message { return &WaitOperationRequest{} }, func() ([]byte, []int) { return (*WaitOperationRequest)(nil).Descriptor() }},
		{"CancelOperationRequest", func() proto.Message { return &CancelOperationRequest{} }, func() ([]byte, []int) { return (*CancelOperationRequest)(nil).Descriptor() }},
		{"Operation", func() proto.Message { return &Operation{} }, func() ([]byte, []int) { return (*Operation)(nil).Descriptor() }},
		{"RegisterWorkerRequest", func() proto.Message { return &RegisterWorkerRequest{} }, func() ([]byte, []int) { return (*RegisterWorkerRequest)(nil).Descriptor() }},
		{"WorkerRegistration", func() proto.Message { return &WorkerRegistration{} }, func() ([]byte, []int) { return (*WorkerRegistration)(nil).Descriptor() }},
		{"RegisterWorkerResponse", func() proto.Message { return &RegisterWorkerResponse{} }, func() ([]byte, []int) { return (*RegisterWorkerResponse)(nil).Descriptor() }},
		{"LeaseOperationRequest", func() proto.Message { return &LeaseOperationRequest{} }, func() ([]byte, []int) { return (*LeaseOperationRequest)(nil).Descriptor() }},
		{"WorkerAssignment", func() proto.Message { return &WorkerAssignment{} }, func() ([]byte, []int) { return (*WorkerAssignment)(nil).Descriptor() }},
		{"LeaseOperationResponse", func() proto.Message { return &LeaseOperationResponse{} }, func() ([]byte, []int) { return (*LeaseOperationResponse)(nil).Descriptor() }},
		{"HeartbeatOperationRequest", func() proto.Message { return &HeartbeatOperationRequest{} }, func() ([]byte, []int) { return (*HeartbeatOperationRequest)(nil).Descriptor() }},
		{"HeartbeatOperationResponse", func() proto.Message { return &HeartbeatOperationResponse{} }, func() ([]byte, []int) { return (*HeartbeatOperationResponse)(nil).Descriptor() }},
		{"CompleteOperationRequest", func() proto.Message { return &CompleteOperationRequest{} }, func() ([]byte, []int) { return (*CompleteOperationRequest)(nil).Descriptor() }},
		{"CompleteOperationResponse", func() proto.Message { return &CompleteOperationResponse{} }, func() ([]byte, []int) { return (*CompleteOperationResponse)(nil).Descriptor() }},
		{"DisconnectWorkerRequest", func() proto.Message { return &DisconnectWorkerRequest{} }, func() ([]byte, []int) { return (*DisconnectWorkerRequest)(nil).Descriptor() }},
		{"DisconnectWorkerResponse", func() proto.Message { return &DisconnectWorkerResponse{} }, func() ([]byte, []int) { return (*DisconnectWorkerResponse)(nil).Descriptor() }},
	}
}

// dapbNonZero builds a non-zero value settable into a generated field of type
// ft. Generated messages only use a small closed set of field kinds.
func dapbNonZero(t *testing.T, ft reflect.Type) reflect.Value {
	t.Helper()
	switch ft.Kind() {
	case reflect.String:
		return reflect.ValueOf("dapb-nonzero").Convert(ft)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(ft)
	case reflect.Int32:
		return reflect.ValueOf(int32(-13)).Convert(ft)
	case reflect.Int64:
		return reflect.ValueOf(int64(1234567)).Convert(ft)
	case reflect.Uint32:
		return reflect.ValueOf(uint32(42)).Convert(ft)
	case reflect.Slice:
		if ft.Elem().Kind() == reflect.Uint8 {
			return reflect.ValueOf([]byte("dapb-bytes")).Convert(ft)
		}
		return reflect.ValueOf([]string{"dapb-a", "dapb-b"}).Convert(ft)
	case reflect.Map:
		return reflect.ValueOf(map[string]string{"dapb-k": "dapb-v"}).Convert(ft)
	case reflect.Pointer:
		return reflect.New(ft.Elem())
	default:
		t.Fatalf("dapbNonZero: unhandled field kind %s for type %s", ft.Kind(), ft)
		return reflect.Value{}
	}
}

// dapbSetAllFields fills every exported field of m with a non-zero value.
func dapbSetAllFields(t *testing.T, m proto.Message) {
	t.Helper()
	rv := reflect.ValueOf(m).Elem()
	for i := 0; i < rv.NumField(); i++ {
		f := rv.Type().Field(i)
		if f.PkgPath != "" {
			continue
		}
		rv.Field(i).Set(dapbNonZero(t, f.Type))
	}
}

// dapbExportedFields returns the exported (proto-generated) field names of the
// message type behind m.
func dapbExportedFields(m proto.Message) []string {
	rt := reflect.TypeOf(m).Elem()
	var names []string
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).PkgPath == "" {
			names = append(names, rt.Field(i).Name)
		}
	}
	return names
}

func dapbFullName(name string) protoreflect.FullName {
	return protoreflect.FullName("wb.daemon.v1." + name)
}

// TestDapbMessageDescriptorAndProtoReflect asserts that each generated message
// exposes a real descriptor, that the deprecated Descriptor method returns the
// gzipped file descriptor plus the right index, and that ProtoReflect works for
// both an allocated message and a nil pointer receiver.
func TestDapbMessageDescriptorAndProtoReflect(t *testing.T) {
	cases := dapbMessageCases()
	if len(cases) != 19 {
		t.Fatalf("expected 19 named message cases, got %d", len(cases))
	}
	seen := make(map[string]bool, len(cases))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if seen[tc.name] {
				t.Fatalf("duplicate case %q", tc.name)
			}
			seen[tc.name] = true

			m := tc.fresh()
			pm := m.ProtoReflect()
			if pm == nil {
				t.Fatalf("%s.ProtoReflect() returned nil", tc.name)
			}
			if got, want := pm.Descriptor().FullName(), dapbFullName(tc.name); got != want {
				t.Errorf("descriptor full name = %q, want %q", got, want)
			}
			if got, want := pm.Descriptor().Fields().Len(), len(dapbExportedFields(m)); got != want {
				t.Errorf("descriptor field count = %d, want %d", got, want)
			}
			if !pm.IsValid() {
				t.Errorf("%s.ProtoReflect() of allocated message is invalid", tc.name)
			}

			raw, idx := tc.desc()
			if len(idx) != 1 || idx[0] != i {
				t.Errorf("Descriptor() index = %v, want [%d]", idx, i)
			}
			if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
				t.Errorf("Descriptor() bytes are not gzip compressed: % x", raw[:min(4, len(raw))])
			}
			if top := File_wb_daemon_v1_daemon_proto.Messages().Get(i); top.FullName() != dapbFullName(tc.name) {
				t.Errorf("File.Messages().Get(%d) = %s, want %s", i, top.FullName(), dapbFullName(tc.name))
			}

			// A nil pointer receiver must still yield a usable (but invalid)
			// reflective view rather than panicking.
			nilMsg := reflect.Zero(reflect.TypeOf(m)).Interface().(proto.Message)
			nilPM := nilMsg.ProtoReflect()
			if nilPM == nil {
				t.Fatalf("nil %s.ProtoReflect() returned nil", tc.name)
			}
			if nilPM.IsValid() {
				t.Errorf("nil %s.ProtoReflect() reported a valid message", tc.name)
			}
			if got, want := nilPM.Descriptor().FullName(), dapbFullName(tc.name); got != want {
				t.Errorf("nil descriptor full name = %q, want %q", got, want)
			}
		})
	}
}

// TestDapbGettersReturnFieldValues sets every exported field and checks that the
// corresponding generated getter returns exactly that value.
func TestDapbGettersReturnFieldValues(t *testing.T) {
	for _, tc := range dapbMessageCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := tc.fresh()
			dapbSetAllFields(t, m)
			rv := reflect.ValueOf(m).Elem()
			exercised := 0
			for i := 0; i < rv.NumField(); i++ {
				f := rv.Type().Field(i)
				if f.PkgPath != "" {
					continue
				}
				getter := reflect.ValueOf(m).MethodByName("Get" + f.Name)
				if !getter.IsValid() {
					t.Fatalf("missing generated getter Get%s", f.Name)
				}
				out := getter.Call(nil)
				if len(out) != 1 {
					t.Fatalf("Get%s returned %d values, want 1", f.Name, len(out))
				}
				want := rv.Field(i).Interface()
				if got := out[0].Interface(); !reflect.DeepEqual(got, want) {
					t.Errorf("Get%s() = %#v, want %#v", f.Name, got, want)
				}
				exercised++
			}
			if exercised != len(dapbExportedFields(m)) {
				t.Fatalf("exercised %d getters, want %d", exercised, len(dapbExportedFields(m)))
			}

			// Assert the deprecated v1 interface surface is still satisfied and
			// that its marker method is callable.
			legacy, ok := m.(dapbLegacyMessage)
			if !ok {
				t.Fatalf("%s does not implement the deprecated proto v1 message interface", tc.name)
			}
			legacy.ProtoMessage()

			// String() must render the populated message in a form that parses
			// back to an equal message.
			text := legacy.String()
			if exercised > 0 && strings.TrimSpace(text) == "" {
				t.Fatalf("String() of populated %s is empty", tc.name)
			}
			parsed := tc.fresh()
			if err := prototext.Unmarshal([]byte(text), parsed); err != nil {
				t.Fatalf("String() output %q is not parseable proto text: %v", text, err)
			}
			if !proto.Equal(m, parsed) {
				t.Errorf("String() text round-trip mismatch:\n got %s\nwant %s", parsed, m)
			}
		})
	}
}

// TestDapbGettersAreNilSafe calls every generated getter on a nil receiver and
// asserts it returns the field's zero value instead of panicking.
func TestDapbGettersAreNilSafe(t *testing.T) {
	for _, tc := range dapbMessageCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ptrType := reflect.TypeOf(tc.fresh())
			rv := reflect.Zero(ptrType)
			if !rv.IsNil() {
				t.Fatalf("expected a nil pointer for %s", tc.name)
			}
			getters := 0
			for i := 0; i < ptrType.NumMethod(); i++ {
				method := ptrType.Method(i)
				if !strings.HasPrefix(method.Name, "Get") {
					continue
				}
				getters++
				out := rv.Method(i).Call(nil)
				if len(out) != 1 {
					t.Fatalf("%s returned %d values, want 1", method.Name, len(out))
				}
				if !out[0].IsZero() {
					t.Errorf("nil %s.%s() = %#v, want zero value", tc.name, method.Name, out[0].Interface())
				}
			}
			if want := len(dapbExportedFields(tc.fresh())); getters != want {
				t.Errorf("%s has %d getters, want %d (one per exported field)", tc.name, getters, want)
			}
		})
	}
}

// TestDapbResetClearsFields asserts Reset returns the message to its zero value.
func TestDapbResetClearsFields(t *testing.T) {
	for _, tc := range dapbMessageCases() {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.fresh()
			dapbSetAllFields(t, m)
			rv := reflect.ValueOf(m).Elem()
			m.(dapbLegacyMessage).Reset()
			for i := 0; i < rv.NumField(); i++ {
				f := rv.Type().Field(i)
				if f.PkgPath != "" {
					continue
				}
				if !rv.Field(i).IsZero() {
					t.Errorf("Reset left field %s = %#v, want zero", f.Name, rv.Field(i).Interface())
				}
			}
			if !proto.Equal(m, tc.fresh()) {
				t.Errorf("Reset %s not equal to a fresh zero message", tc.name)
			}
			if pm := m.ProtoReflect(); !pm.IsValid() || pm.Descriptor().FullName() != dapbFullName(tc.name) {
				t.Errorf("ProtoReflect after Reset is not the expected message")
			}
		})
	}
}

// TestDapbMarshalUnmarshalRoundTrip asserts the generated messages survive a
// binary protobuf round trip with every field populated.
func TestDapbMarshalUnmarshalRoundTrip(t *testing.T) {
	for _, tc := range dapbMessageCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := tc.fresh()
			dapbSetAllFields(t, src)
			blob, err := proto.Marshal(src)
			if err != nil {
				t.Fatalf("Marshal(%s) error: %v", tc.name, err)
			}
			if len(dapbExportedFields(src)) > 0 && len(blob) == 0 {
				t.Fatalf("Marshal(%s) produced no bytes for a populated message", tc.name)
			}
			dst := tc.fresh()
			if err := proto.Unmarshal(blob, dst); err != nil {
				t.Fatalf("Unmarshal(%s) error: %v", tc.name, err)
			}
			if !proto.Equal(src, dst) {
				t.Errorf("%s round trip mismatch:\n got %v\nwant %v", tc.name, dst, src)
			}
		})
	}
}

// TestDapbEnumAccessors asserts the generated accessors of both enums agree with
// the generated name/value maps, and that unknown numbers still format.
func TestDapbEnumAccessors(t *testing.T) {
	t.Run("DaemonState", func(t *testing.T) {
		if got, want := len(DaemonState_name), 3; got != want {
			t.Fatalf("DaemonState_name has %d entries, want %d", got, want)
		}
		for number, name := range DaemonState_name {
			val := DaemonState(number)
			if got := DaemonState_value[name]; got != number {
				t.Errorf("DaemonState_value[%q] = %d, want %d", name, got, number)
			}
			if got := val.String(); got != name {
				t.Errorf("DaemonState(%d).String() = %q, want %q", number, got, name)
			}
			if got := int32(val.Number()); got != number {
				t.Errorf("DaemonState(%d).Number() = %d", number, got)
			}
			if got := val.Enum(); got == nil || *got != val {
				t.Errorf("DaemonState(%d).Enum() = %v, want %v", number, got, val)
			}
			if got := val.Type().New(val.Number()); got != protoreflect.Enum(val) {
				t.Errorf("DaemonState(%d).Type().New() = %v, want %v", number, got, val)
			}
		}
		desc := DaemonState(0).Descriptor()
		if got, want := desc.FullName(), protoreflect.FullName("wb.daemon.v1.DaemonState"); got != want {
			t.Errorf("DaemonState descriptor full name = %q, want %q", got, want)
		}
		if got, want := desc.Values().Len(), len(DaemonState_name); got != want {
			t.Errorf("DaemonState descriptor values = %d, want %d", got, want)
		}
		if got := DaemonState(1234).String(); got != "1234" {
			t.Errorf("unknown DaemonState String() = %q, want %q", got, "1234")
		}
		if raw, idx := DaemonState(0).EnumDescriptor(); len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b || len(idx) != 1 || idx[0] != 0 {
			t.Errorf("DaemonState.EnumDescriptor() = (% x, %v)", raw[:min(4, len(raw))], idx)
		}
	})

	t.Run("OperationState", func(t *testing.T) {
		if got, want := len(OperationState_name), 7; got != want {
			t.Fatalf("OperationState_name has %d entries, want %d", got, want)
		}
		for number, name := range OperationState_name {
			val := OperationState(number)
			if got := OperationState_value[name]; got != number {
				t.Errorf("OperationState_value[%q] = %d, want %d", name, got, number)
			}
			if got := val.String(); got != name {
				t.Errorf("OperationState(%d).String() = %q, want %q", number, got, name)
			}
			if got := int32(val.Number()); got != number {
				t.Errorf("OperationState(%d).Number() = %d", number, got)
			}
			if got := val.Enum(); got == nil || *got != val {
				t.Errorf("OperationState(%d).Enum() = %v, want %v", number, got, val)
			}
			if got := val.Type().New(val.Number()); got != protoreflect.Enum(val) {
				t.Errorf("OperationState(%d).Type().New() = %v, want %v", number, got, val)
			}
		}
		desc := OperationState(0).Descriptor()
		if got, want := desc.FullName(), protoreflect.FullName("wb.daemon.v1.OperationState"); got != want {
			t.Errorf("OperationState descriptor full name = %q, want %q", got, want)
		}
		if got, want := desc.Values().Len(), len(OperationState_name); got != want {
			t.Errorf("OperationState descriptor values = %d, want %d", got, want)
		}
		if got := OperationState(4321).String(); got != "4321" {
			t.Errorf("unknown OperationState String() = %q, want %q", got, "4321")
		}
		if raw, idx := OperationState(0).EnumDescriptor(); len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b || len(idx) != 1 || idx[0] != 1 {
			t.Errorf("OperationState.EnumDescriptor() = (% x, %v)", raw[:min(4, len(raw))], idx)
		}
	})
}

// TestDapbFileDescriptor asserts the file descriptor carries the two enums and
// the single service declared in daemon.proto.
func TestDapbFileDescriptor(t *testing.T) {
	t.Parallel()
	fd := File_wb_daemon_v1_daemon_proto
	if fd == nil {
		t.Fatal("File_wb_daemon_v1_daemon_proto is nil")
	}
	if got, want := fd.Path(), "wb/daemon/v1/daemon.proto"; got != want {
		t.Errorf("file path = %q, want %q", got, want)
	}
	if got, want := fd.Package(), protoreflect.FullName("wb.daemon.v1"); got != want {
		t.Errorf("package = %q, want %q", got, want)
	}
	if got, want := fd.Enums().Len(), 2; got != want {
		t.Errorf("enum count = %d, want %d", got, want)
	}
	if got, want := fd.Enums().ByName("DaemonState").Values().Len(), 3; got != want {
		t.Errorf("DaemonState values = %d, want %d", got, want)
	}
	if got, want := fd.Enums().ByName("OperationState").Values().Len(), 7; got != want {
		t.Errorf("OperationState values = %d, want %d", got, want)
	}
	if fd.Messages().Len() == 0 {
		t.Fatal("file descriptor has no top-level messages")
	}
	nested := fd.Messages().ByName("SubmitOperationRequest").Messages()
	if nested.Len() != 1 || nested.Get(0).Name() != "EnvironmentEntry" {
		t.Errorf("SubmitOperationRequest nested messages = %v, want EnvironmentEntry", nested)
	}
	if got, want := fd.Services().Len(), 1; got != want {
		t.Fatalf("service count = %d, want %d", got, want)
	}
	svc := fd.Services().ByName("DaemonService")
	if svc == nil {
		t.Fatal("DaemonService not found in file descriptor")
	}
	wantMethods := map[string][2]string{
		"GetDaemonInfo":      {"GetDaemonInfoRequest", "GetDaemonInfoResponse"},
		"SubmitOperation":    {"SubmitOperationRequest", "Operation"},
		"GetOperation":       {"GetOperationRequest", "Operation"},
		"WaitOperation":      {"WaitOperationRequest", "Operation"},
		"CancelOperation":    {"CancelOperationRequest", "Operation"},
		"RegisterWorker":     {"RegisterWorkerRequest", "RegisterWorkerResponse"},
		"LeaseOperation":     {"LeaseOperationRequest", "LeaseOperationResponse"},
		"HeartbeatOperation": {"HeartbeatOperationRequest", "HeartbeatOperationResponse"},
		"CompleteOperation":  {"CompleteOperationRequest", "CompleteOperationResponse"},
		"DisconnectWorker":   {"DisconnectWorkerRequest", "DisconnectWorkerResponse"},
	}
	if got, want := svc.Methods().Len(), len(wantMethods); got != want {
		t.Fatalf("DaemonService method count = %d, want %d", got, want)
	}
	for name, types := range wantMethods {
		method := svc.Methods().ByName(protoreflect.Name(name))
		if method == nil {
			t.Errorf("method %s not found", name)
			continue
		}
		if got, want := method.Input().FullName(), dapbFullName(types[0]); got != want {
			t.Errorf("%s input = %s, want %s", name, got, want)
		}
		if got, want := method.Output().FullName(), dapbFullName(types[1]); got != want {
			t.Errorf("%s output = %s, want %s", name, got, want)
		}
		if method.IsStreamingClient() || method.IsStreamingServer() {
			// All RPCs here are unary; both must be false.
			t.Errorf("%s unexpectedly reported as streaming", name)
		}
	}
}

// TestDapbFileInitIsIdempotent re-runs the generated init and asserts it leaves
// the already-built descriptor untouched.
func TestDapbFileInitIsIdempotent(t *testing.T) {
	if file_wb_daemon_v1_daemon_proto_goTypes != nil {
		t.Fatal("generated init should have released goTypes after building")
	}
	if file_wb_daemon_v1_daemon_proto_depIdxs != nil {
		t.Fatal("generated init should have released depIdxs after building")
	}
	before := File_wb_daemon_v1_daemon_proto
	file_wb_daemon_v1_daemon_proto_init()
	if File_wb_daemon_v1_daemon_proto != before {
		t.Error("second file init replaced the built file descriptor")
	}
	if got, want := File_wb_daemon_v1_daemon_proto.Services().Len(), 1; got != want {
		t.Errorf("service count after second init = %d, want %d", got, want)
	}
}

// TestDapbEnumStringMatchesDescriptor cross-checks enum String() against the
// descriptor values so a stale name map cannot pass unnoticed.
func TestDapbEnumStringMatchesDescriptor(t *testing.T) {
	values := DaemonState(0).Descriptor().Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if got := DaemonState(v.Number()).String(); got != string(v.Name()) {
			t.Errorf("DaemonState(%d).String() = %q, want %q", v.Number(), got, v.Name())
		}
	}
	values = OperationState(0).Descriptor().Values()
	for i := 0; i < values.Len(); i++ {
		v := values.Get(i)
		if got := OperationState(v.Number()).String(); got != string(v.Name()) {
			t.Errorf("OperationState(%d).String() = %q, want %q", v.Number(), got, v.Name())
		}
	}
}

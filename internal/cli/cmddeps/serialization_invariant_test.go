package cmddeps

import (
	"encoding"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"gopkg.in/yaml.v3"
)

// This guard binds the ignored encoding errors to actual concrete producer
// types. A future unsupported field or custom marshaler must reopen the policy.
func TestClosedDependencyReportsOnlyNormalizeToSupportedJSONValues(t *testing.T) {
	t.Parallel()
	for _, report := range []any{deps.Report{}, deps.BumpReport{}, deps.DriftReport{}, deps.PeerReport{}} {
		typ := reflect.TypeOf(report)
		t.Run(typ.Name(), func(t *testing.T) {
			t.Parallel()
			checkReportType(t, typ, map[reflect.Type]bool{})
		})
	}
}
func checkReportType(t *testing.T, typ reflect.Type, ancestors map[reflect.Type]bool) {
	t.Helper()
	if typ == reflect.TypeFor[time.Time]() {
		return
	}
	if ancestors[typ] {
		t.Fatalf("recursive report type %v can form encoder cycles", typ)
	}
	for _, contract := range []reflect.Type{reflect.TypeFor[yaml.Marshaler](), reflect.TypeFor[json.Marshaler](), reflect.TypeFor[encoding.TextMarshaler]()} {
		if typ.Implements(contract) || reflect.PointerTo(typ).Implements(contract) {
			t.Fatalf("custom marshaler %v invalidates concrete encoding proof", typ)
		}
	}
	ancestors[typ] = true
	defer delete(ancestors, typ)
	switch typ.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
	case reflect.Pointer, reflect.Slice, reflect.Array:
		checkReportType(t, typ.Elem(), ancestors)
	case reflect.Map:
		if typ.Key().Kind() != reflect.String {
			t.Fatalf("non-string map keys in %v", typ)
		}
		checkReportType(t, typ.Key(), ancestors)
		checkReportType(t, typ.Elem(), ancestors)
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.IsExported() && field.Tag.Get("yaml") != "-" {
				checkReportType(t, field.Type, ancestors)
			}
		}
	default:
		t.Fatalf("unsupported report field type %v (%v)", typ, typ.Kind())
	}
}

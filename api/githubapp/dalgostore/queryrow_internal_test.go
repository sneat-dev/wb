package dalgostore

import (
	"reflect"
	"testing"
)

// TestQueryRowDecodesGenericRowsAndReportsBadOnes covers the adapter
// independence rule: a row that arrives as the caller's pointer type is used
// as is, a generic map row is decoded through JSON, and rows that cannot be
// encoded or do not fit the element type are reported, not panicked on.
func TestQueryRowDecodesGenericRowsAndReportsBadOnes(t *testing.T) {
	type row struct {
		Value int `json:"value"`
	}
	element := reflect.TypeOf(row{})
	direct, err := queryRow(&row{Value: 7}, element)
	if err != nil || direct.Interface().(row).Value != 7 {
		t.Fatalf("pointer row = %v, %v", direct, err)
	}
	generic, err := queryRow(map[string]any{"value": 9}, element)
	if err != nil || generic.Interface().(row).Value != 9 {
		t.Fatalf("map row = %v, %v", generic, err)
	}
	if _, err := queryRow(map[string]any{"value": make(chan int)}, element); err == nil {
		t.Fatal("unencodable row must be reported")
	}
	if _, err := queryRow(map[string]any{"value": "nine"}, element); err == nil {
		t.Fatal("mistyped row must be reported")
	}
}

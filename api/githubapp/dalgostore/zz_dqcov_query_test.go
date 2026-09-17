// Copyright 2026 Sneat Co.

package dalgostore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"

	"github.com/sneat-dev/wb/api/githubapp/dalgostore"
)

// dqCovGenericRowsDB models the adapter that rebuilds query rows generically
// instead of honouring the record factory: it hands back raw map[string]any
// values, which is the case dalgostore.queryRow exists to absorb. It embeds the
// dal.DB interface so it keeps the full method set and only replaces reads.
type dqCovGenericRowsDB struct {
	dal.DB
	rows []any
}

func (db dqCovGenericRowsDB) ExecuteQueryToRecordsReader(context.Context, dal.Query) (dal.RecordsReader, error) {
	records := make([]record.Record, 0, len(db.rows))
	for index, row := range db.rows {
		key := record.NewKeyWithID("widgets", fmt.Sprintf("w%d", index))
		records = append(records, record.NewRecordWithData(key, row))
	}
	return &dqCovRecordReader{records: records}, nil
}

type dqCovRecordReader struct {
	records []record.Record
	index   int
}

func (reader *dqCovRecordReader) Cursor() (string, error) { return "", nil }

func (reader *dqCovRecordReader) Close() error { return nil }

func (reader *dqCovRecordReader) Next() (record.Record, error) {
	if reader.index >= len(reader.records) {
		return nil, dal.ErrNoMoreRecords
	}
	current := reader.records[reader.index]
	reader.index++
	return current, nil
}

// TestDqCovQueryReportsAGenericRowItCannotDecode covers the adapter
// independence rule for a row that the JSON fallback cannot fit into the
// caller's element type: Query must report the failure and leave the target
// slice untouched rather than storing a half-decoded result.
func TestDqCovQueryReportsAGenericRowItCannotDecode(t *testing.T) {
	store := dalgostore.New(dqCovGenericRowsDB{rows: []any{
		map[string]any{"name": "alpha", "kind": "gear", "count": 3},
		map[string]any{"name": "beta", "kind": "spring", "count": "not-a-number"},
	}})
	target := []widget{{Name: "stale"}}
	err := store.Query(context.Background(), "widgets", map[string]any{"kind": "gear"}, 10, &target)
	if err == nil {
		t.Fatal("Query must report a row that cannot be decoded into the caller's element type")
	}
	if !strings.Contains(err.Error(), "query widgets") || !strings.Contains(err.Error(), "decode query row into") {
		t.Fatalf("Query error = %v, want the collection and the decode failure", err)
	}
	if len(target) != 1 || target[0].Name != "stale" {
		t.Fatalf("Query replaced the target slice with %+v despite failing", target)
	}
}

// TestDqCovQueryDecodesGenericRowsIntoTheTargetSlice is the positive half of
// the same contract: an adapter that returns generic map rows still produces
// the caller's structs.
func TestDqCovQueryDecodesGenericRowsIntoTheTargetSlice(t *testing.T) {
	store := dalgostore.New(dqCovGenericRowsDB{rows: []any{
		map[string]any{"name": "alpha", "kind": "gear", "count": 3},
		map[string]any{"name": "beta", "kind": "spring", "count": 5},
	}})
	var got []widget
	if err := store.Query(context.Background(), "widgets", nil, 0, &got); err != nil {
		t.Fatalf("Query: %v", err)
	}
	want := []widget{{Name: "alpha", Kind: "gear", Count: 3}, {Name: "beta", Kind: "spring", Count: 5}}
	if len(got) != len(want) {
		t.Fatalf("Query returned %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("Query returned %+v, want %+v", got, want)
		}
	}
}

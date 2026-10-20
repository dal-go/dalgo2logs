package dalgo2logs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

func fixture(t *testing.T, format Format, content string) (dal.DB, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "events.log")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := NewDB(Config{ID: "test", Root: root, Sources: []Source{{Collection: "events", Path: "events.log", Format: format}}})
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := db.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	return db, path
}

func rows(t *testing.T, db dal.DB, q dal.StructuredQuery) []record.Record {
	t.Helper()
	r, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []record.Record
	for {
		rec, err := r.Next()
		if err == dal.ErrNoMoreRecords {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

func query(where ...dal.Condition) dal.StructuredQuery {
	return dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Where(where...).OrderBy(dal.AscendingField("timestamp")).SelectIntoRecord(nil)
}

func TestJSONLFilteringProvenanceAndRotation(t *testing.T) {
	content := `{"timestamp":"2026-10-19T04:00:00+01:00","severity":"info","service":"checkout","order_id":"11039","correlation_id":"ord-11039","message":"queued","extra":{"attempt":1}}` + "\n" +
		`{"timestamp":"2026-10-19T03:01:00Z","severity":"ERROR","service":"shipping","order_id":"11039","correlation_id":"ord-11039","message":"carrier rejected"}` + "\n" +
		`{"time":"bad","level":"warn","message":"bad clock"}` + "\n" +
		`not json` + "\n"
	db, path := fixture(t, JSONL, content)
	all := rows(t, db, query())
	if len(all) != 4 {
		t.Fatalf("got %d entries", len(all))
	}
	first := all[0].Data().(map[string]any)
	if first["timestamp"] != "2026-10-19T03:00:00Z" || first["severity"] != "INFO" || first["order_id"] != "11039" {
		t.Fatalf("normalized entry: %#v", first)
	}
	if !strings.Contains(first["fields_json"].(string), `"attempt":1`) || !strings.Contains(first["raw"].(string), `"extra"`) {
		t.Fatalf("lost original payload: %#v", first)
	}
	if !strings.Contains(first["source_ref"].(string), ":0:") {
		t.Fatalf("bad source ref: %#v", first["source_ref"])
	}
	if got := rows(t, db, query(dal.WhereField("source_ref", dal.Equal, first["source_ref"].(string)))); len(got) != 1 || got[0].Key().ID != all[0].Key().ID {
		t.Fatalf("source reference filter returned %#v", got)
	}
	if got := len(rows(t, db, query(dal.WhereField("severity", dal.Equal, "ERROR"), dal.WhereField("order_id", dal.Equal, "11039")))); got != 1 {
		t.Fatalf("filtered rows = %d", got)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rotated := rows(t, db, query())
	if rotated[0].Key().ID == all[0].Key().ID {
		t.Fatal("content change did not change evidence ID")
	}
}

func TestTimestampPrecisionOrderingAndRange(t *testing.T) {
	db, _ := fixture(t, JSONL, `{"timestamp":"2018-04-24T09:13:05Z","message":"whole"}`+"\n"+
		`{"timestamp":"2018-04-24T09:13:05.300Z","message":"fraction"}`+"\n"+
		`{"timestamp":"2018-04-24T09:13:06Z","message":"next"}`+"\n")
	all := rows(t, db, query())
	for i, want := range []string{"whole", "fraction", "next"} {
		if got := all[i].Data().(map[string]any)["message"]; got != want {
			t.Fatalf("row %d: got %v, want %q", i, got, want)
		}
	}
	filtered := rows(t, db, query(
		dal.WhereField("timestamp", dal.GreaterOrEqual, "2018-04-24T09:13:05.100Z"),
		dal.WhereField("timestamp", dal.LessThen, "2018-04-24T09:13:06Z"),
	))
	if len(filtered) != 1 || filtered[0].Data().(map[string]any)["message"] != "fraction" {
		t.Fatalf("fractional range: %#v", filtered)
	}
}

func TestTextMultilineMalformedAndMissingTime(t *testing.T) {
	db, _ := fixture(t, Text, "2026-10-19T03:00:00Z INFO orders order_id=11039 correlation_id=c1 accepted\n"+
		"2026-10-19T03:01:00Z ERROR shipping order_id=11039 correlation_id=c1 carrier failed\n  at Carrier.Send(Carrier.java:91)\n"+
		"not timestamped\n")
	got := rows(t, db, query())
	if len(got) != 3 {
		t.Fatalf("rows = %d", len(got))
	}
	data := got[1].Data().(map[string]any)
	if data["line_start"] != 2 || data["line_end"] != 3 || !strings.Contains(data["raw"].(string), "Carrier.Send") {
		t.Fatalf("multiline lost: %#v", data)
	}
	if got[2].Data().(map[string]any)["parse_status"] != "malformed" {
		t.Fatal("malformed entry hidden")
	}
}

func TestSecurityBoundsAndUnsupported(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.log")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret.log", outside} {
		if _, err := NewDB(Config{ID: "x", Root: root, Sources: []Source{{Collection: "events", Path: path, Format: JSONL}}}); err == nil {
			t.Fatalf("accepted forbidden path %q", path)
		}
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDB(Config{ID: "x", Root: root, Sources: []Source{{Collection: "events", Path: "link.log", Format: JSONL}}}); err == nil {
		t.Fatal("accepted symlink")
	}
	db, path := fixture(t, JSONL, "{}\n")
	aliased := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().SelectColumns(dal.Column{Alias: "explanation", Expression: dal.Field("message")})
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), aliased); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("aliased projection must be rejected: %v", err)
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().WhereField("message", dal.Equal, "x").SelectIntoRecord(nil)); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("unsupported filter: %v", err)
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().Offset(1).SelectIntoRecord(nil)); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("offset: %v", err)
	}
	if err := db.RunReadwriteTransaction(context.Background(), func(context.Context, dal.ReadwriteTransaction) error { t.Fatal("write worker ran"); return nil }); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 1024)), 0o600); err != nil {
		t.Fatal(err)
	}
	db2, err := NewDB(Config{ID: "test", Root: filepath.Dir(path), Sources: []Source{{Collection: "events", Path: filepath.Base(path), Format: JSONL}}, MaxFileBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db2.ExecuteQueryToRecordsReader(context.Background(), query()); err == nil {
		t.Fatal("accepted oversized file")
	}
	if err := os.WriteFile(path, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entryBounded, err := NewDB(Config{ID: "test", Root: filepath.Dir(path), Sources: []Source{{Collection: "events", Path: filepath.Base(path), Format: JSONL}}, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entryBounded.ExecuteQueryToRecordsReader(context.Background(), query(dal.WhereField("order_id", dal.Equal, "no-match"))); err == nil {
		t.Fatal("filter bypassed scanned entry limit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ExecuteQueryToRecordsReader(ctx, query()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestFileQueryConditionBudgets(t *testing.T) {
	db, path := fixture(t, JSONL, `{"timestamp":"2026-10-19T03:00:00Z","order_id":"11039"}`+"\n")

	comparisons := func(count int) []dal.Condition {
		conditions := make([]dal.Condition, count)
		for i := range conditions {
			conditions[i] = dal.WhereField("order_id", dal.Equal, "11039")
		}
		return conditions
	}
	if got := len(rows(t, db, query(comparisons(maxFilterComparisons)...))); got != 1 {
		t.Fatalf("query with %d comparisons returned %d rows", maxFilterComparisons, got)
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query(comparisons(maxFilterComparisons+1)...)); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("query with %d comparisons: got %v, want dal.ErrNotSupported", maxFilterComparisons+1, err)
	}

	nestedAnd := func(depth int) dal.Condition {
		var condition dal.Condition = dal.NewGroupCondition(dal.And)
		for i := 1; i < depth; i++ {
			condition = dal.NewGroupCondition(dal.And, condition)
		}
		return condition
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query(nestedAnd(maxAndNestingDepth))); err != nil {
		t.Fatalf("AND depth %d: %v", maxAndNestingDepth, err)
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query(nestedAnd(maxAndNestingDepth+1))); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("AND depth %d: got %v, want dal.ErrNotSupported", maxAndNestingDepth+1, err)
	}

	wideEmptyGroups := func(nodeCount int) dal.Condition {
		children := make([]dal.Condition, nodeCount-1)
		for i := range children {
			children[i] = dal.NewGroupCondition(dal.And)
		}
		return dal.NewGroupCondition(dal.And, children...)
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query(wideEmptyGroups(maxConditionNodes))); err != nil {
		t.Fatalf("condition node count %d: %v", maxConditionNodes, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query(wideEmptyGroups(maxConditionNodes+1))); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("condition node count %d: got %v, want dal.ErrNotSupported before file access", maxConditionNodes+1, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ExecuteQueryToRecordsReader(canceled, query(comparisons(maxFilterComparisons+1)...)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled over-budget query: got %v, want context.Canceled", err)
	}
}

func TestSchemaDiscovery(t *testing.T) {
	db, _ := fixture(t, JSONL, "{}\n")
	refs, err := dbschema.ListCollections(context.Background(), db, nil)
	if err != nil || len(refs) != 1 || refs[0].Name() != "events" {
		t.Fatalf("discovery: %v, %#v", err, refs)
	}
	def, err := dbschema.DescribeCollection(context.Background(), db, &refs[0])
	if err != nil || def.Name != "events" || len(def.Fields) < 10 {
		t.Fatalf("schema: %v, %#v", err, def)
	}
}

func BenchmarkBoundedFileQuery(b *testing.B) {
	root := b.TempDir()
	path := filepath.Join(root, "events.jsonl")
	line := []byte(`{"timestamp":"2026-10-19T03:00:00Z","severity":"INFO","service":"orders","order_id":"noise","message":"unrelated"}` + "\n")
	content := make([]byte, 0, len(line)*20000)
	for i := 0; i < 20000; i++ {
		content = append(content, line...)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		b.Fatal(err)
	}
	db, err := NewDB(Config{ID: "bench", Root: root, Sources: []Source{{Collection: "events", Path: "events.jsonl", Format: JSONL}}})
	if err != nil {
		b.Fatal(err)
	}
	defer func() {
		if closer, ok := db.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}()
	q := query(dal.WhereField("order_id", dal.Equal, "11039"))
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := r.Next(); err != dal.ErrNoMoreRecords {
			b.Fatalf("expected no match, got %v", err)
		}
		if err := r.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

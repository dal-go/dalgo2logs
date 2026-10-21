package dalgo2logs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

func timestampFixture(t *testing.T, content, field string) (dal.DB, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "shipping.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := NewDB(Config{ID: "shipping", Root: root, Sources: []Source{{Collection: "events", Path: "shipping.jsonl", Format: JSONL, TimestampField: field}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.(interface{ Close() error }).Close(); err != nil {
			t.Error(err)
		}
	})
	return db, path
}

func TestTimestampFieldDirectFileProvenanceRangeOrderAndReopen(t *testing.T) {
	late := `{"occurred_at":"2026-10-21T10:30:00.123456789+02:00","timestamp":"2099-01-01T00:00:00Z","order_id":"11045","message":"late"}` + "\n"
	early := `{"occurred_at":"2026-10-21T08:29:59.999999999Z","time":"2099-01-01T00:00:00Z","order_id":"11045","message":"early","extra":{"counter":9007199254740993}}` + "\r\n"
	content := late + early
	db, path := timestampFixture(t, content, "occurred_at")
	collection := dal.NewRootCollectionRef("events", "")
	schema, err := dbschema.DescribeCollection(context.Background(), db, &collection)
	if err != nil {
		t.Fatal(err)
	}
	foundTimestamp := false
	for _, field := range schema.Fields {
		if field.Name == "occurred_at" {
			t.Fatal("trusted mapping leaked into public schema")
		}
		if field.Name == "timestamp" {
			foundTimestamp = field.Type == dbschema.String && field.Nullable
		}
	}
	if !foundTimestamp {
		t.Fatal("normalized timestamp schema changed")
	}
	all := rows(t, db, query(dal.WhereField("order_id", dal.Equal, "11045")))
	if len(all) != 2 {
		t.Fatalf("rows=%d", len(all))
	}
	first := all[0].Data().(map[string]any)
	if first["timestamp"] != "2026-10-21T08:29:59.999999999Z" || all[1].Data().(map[string]any)["timestamp"] != "2026-10-21T08:30:00.123456789Z" {
		t.Fatalf("mapped chronological values: %#v", all)
	}
	digest := sha256.Sum256([]byte(content))
	wantRef := fmt.Sprintf("shipping:events:%s:%d:%d", hex.EncodeToString(digest[:]), len(late), len(content))
	id := sha256.Sum256([]byte(wantRef))
	if first["raw"] != early || first["source_ref"] != wantRef || first["byte_offset"] != int64(len(late)) || first["line_start"] != 2 || first["line_end"] != 2 || first["parser"] != "jsonl" || all[0].Key().ID != hex.EncodeToString(id[:]) {
		t.Fatalf("original evidence identity changed: %#v", first)
	}
	var original map[string]any
	decoder := json.NewDecoder(strings.NewReader(early))
	decoder.UseNumber()
	if err := decoder.Decode(&original); err != nil {
		t.Fatal(err)
	}
	expectedFields, err := json.Marshal(original)
	if err != nil || first["fields_json"] != string(expectedFields) {
		t.Fatalf("original fields changed: %v %#v", err, first["fields_json"])
	}
	reopened := record.NewRecordWithData(all[0].Key(), map[string]any{})
	if err := db.Get(context.Background(), reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.Data().(map[string]any)["source_ref"] != wantRef || reopened.Data().(map[string]any)["raw"] != early {
		t.Fatal("keyed read changed original evidence")
	}
	rangeRows := rows(t, db, query(dal.WhereField("timestamp", dal.GreaterOrEqual, "2026-10-21T08:30:00Z"), dal.WhereField("timestamp", dal.LessThen, "2026-10-21T08:30:00.2Z")))
	if len(rangeRows) != 1 || rangeRows[0].Data().(map[string]any)["message"] != "late" {
		t.Fatalf("mapped time range: %#v", rangeRows)
	}
	descending := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().OrderBy(dal.DescendingField("timestamp")).Limit(1).SelectIntoRecord(nil)
	if limited := rows(t, db, descending); len(limited) != 1 || limited[0].Data().(map[string]any)["message"] != "late" {
		t.Fatalf("mapped order/limit: %#v", limited)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ExecuteQueryToRecordsReader(canceled, query()); !errors.Is(err, context.Canceled) {
		t.Fatalf("mapped cancellation: %v", err)
	}
	replacement := `{"occurred_at":"2026-10-22T00:00:00Z","message":"current"}` + "\n"
	if err := os.WriteFile(path, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	current := rows(t, db, query())
	if len(current) != 1 || current[0].Data().(map[string]any)["timestamp"] != "2026-10-22T00:00:00Z" || current[0].Data().(map[string]any)["source_ref"] == wantRef {
		t.Fatal("mapped source cached old content")
	}
	stale := rows(t, db, query(dal.WhereField("source_ref", dal.Equal, wantRef)))
	if len(stale) != 0 {
		t.Fatal("old evidence reference relabeled current bytes")
	}
}

func TestTimestampFieldAuthoritativeMissingInvalidAndMalformed(t *testing.T) {
	alternate := `"timestamp":"2026-10-21T00:00:00Z","time":"2026-10-21T01:00:00Z"`
	cases := []struct{ name, raw, status, stamp string }{
		{"valid", `{"occurred_at":"2026-10-21T04:30:00.123456789-04:00",` + alternate + `}`, "parsed", "2026-10-21T08:30:00.123456789Z"},
		{"missing", `{` + alternate + `}`, "missing_timestamp", ""},
		{"null", `{"occurred_at":null,` + alternate + `}`, "missing_timestamp", ""},
		{"empty", `{"occurred_at":"",` + alternate + `}`, "invalid_timestamp", ""},
		{"invalid", `{"occurred_at":"bad",` + alternate + `}`, "invalid_timestamp", ""},
		{"number", `{"occurred_at":123,` + alternate + `}`, "invalid_timestamp", ""},
		{"boolean", `{"occurred_at":true,` + alternate + `}`, "invalid_timestamp", ""},
		{"array", `{"occurred_at":["2026-10-21T00:00:00Z"],` + alternate + `}`, "invalid_timestamp", ""},
		{"object", `{"occurred_at":{"time":"2026-10-21T00:00:00Z"},` + alternate + `}`, "invalid_timestamp", ""},
		{"case-sensitive", `{"Occurred_at":"2026-10-21T00:00:00Z",` + alternate + `}`, "missing_timestamp", ""},
		{"malformed", `not json`, "malformed", ""},
		{"trailing", `{"occurred_at":"2026-10-21T00:00:00Z"} {}`, "malformed", ""},
		{"non-object", `[]`, "malformed", ""},
		{"null-object", `null`, "malformed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := timestampFixture(t, tc.raw+"\n", "occurred_at")
			got := rows(t, db, query())
			if len(got) != 1 {
				t.Fatalf("rows=%d", len(got))
			}
			data := got[0].Data().(map[string]any)
			if data["parse_status"] != tc.status || data["raw"] != tc.raw+"\n" {
				t.Fatalf("classification/original bytes: %#v", data)
			}
			value, exists := data["timestamp"]
			if tc.stamp == "" && exists || tc.stamp != "" && value != tc.stamp {
				t.Fatalf("selected timestamp fell back or changed: %#v", data)
			}
		})
	}
}

func TestTimestampFieldZeroValuePreservesLegacySelection(t *testing.T) {
	cases := []struct{ raw, status, stamp string }{
		{`{"timestamp":"2026-10-21T00:00:00Z","time":"2026-10-22T00:00:00Z"}`, "parsed", "2026-10-21T00:00:00Z"},
		{`{"timestamp":null,"time":"2026-10-22T00:00:00Z"}`, "parsed", "2026-10-22T00:00:00Z"},
		{`{"time":"2026-10-22T00:00:00Z"}`, "parsed", "2026-10-22T00:00:00Z"},
		{`{"timestamp":"","time":"2026-10-22T00:00:00Z"}`, "missing_timestamp", ""},
		{`{"timestamp":"bad","time":"2026-10-22T00:00:00Z"}`, "invalid_timestamp", ""},
		{`{"timestamp":123,"time":"2026-10-22T00:00:00Z"}`, "invalid_timestamp", ""},
		{`{"occurred_at":"2026-10-22T00:00:00Z"}`, "missing_timestamp", ""},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			db, _ := timestampFixture(t, tc.raw+"\n", "")
			got := rows(t, db, query())[0].Data().(map[string]any)
			if got["parse_status"] != tc.status {
				t.Fatalf("legacy status: %#v", got)
			}
			stamp, exists := got["timestamp"]
			if tc.stamp == "" && exists || tc.stamp != "" && stamp != tc.stamp {
				t.Fatalf("legacy selection: %#v", got)
			}
		})
	}
}

func TestTimestampFieldConfigurationAndIndependentSources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "events.jsonl"), []byte(`{"occurred_at":"2026-10-21T00:00:00Z","EventAt":"2026-10-22T00:00:00Z","timestamp":"2026-10-23T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	open := func(format Format, field string) (dal.DB, error) {
		return NewDB(Config{ID: "test", Root: root, Sources: []Source{{Collection: "events", Path: "events.jsonl", Format: format, TimestampField: field}}})
	}
	for _, field := range []string{"1time", "occurred.at", "/occurred_at", "time[0]", " timestamp", "timestamp ", "a\nb", "a\x00b", "é", strings.Repeat("a", 129)} {
		if db, err := open(JSONL, field); err == nil {
			db.(interface{ Close() error }).Close()
			t.Fatalf("accepted invalid configured field %q", field)
		}
	}
	if db, err := open(Text, "occurred_at"); err == nil {
		db.(interface{ Close() error }).Close()
		t.Fatal("Text mapping silently ignored")
	}
	for _, tc := range []struct {
		format Format
		field  string
	}{{JSONL, ""}, {JSONL, "_"}, {JSONL, "A0"}, {JSONL, strings.Repeat("a", 128)}, {Text, ""}} {
		db, err := open(tc.format, tc.field)
		if err != nil {
			t.Fatalf("valid configuration %q: %v", tc.field, err)
		}
		if err := db.(interface{ Close() error }).Close(); err != nil {
			t.Fatal(err)
		}
	}
	db, err := NewDB(Config{ID: "multi", Root: root, Sources: []Source{
		{Collection: "occurred", Path: "events.jsonl", Format: JSONL, TimestampField: "occurred_at"},
		{Collection: "event", Path: "events.jsonl", Format: JSONL, TimestampField: "EventAt"},
		{Collection: "legacy", Path: "events.jsonl", Format: JSONL},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer db.(interface{ Close() error }).Close()
	for collection, want := range map[string]string{"occurred": "2026-10-21T00:00:00Z", "event": "2026-10-22T00:00:00Z", "legacy": "2026-10-23T00:00:00Z"} {
		q := dal.From(dal.NewRootCollectionRef(collection, "")).NewQuery().SelectIntoRecord(nil)
		got := rows(t, db, q)
		if len(got) != 1 || got[0].Data().(map[string]any)["timestamp"] != want {
			t.Fatalf("mapping leaked across sources: %s %#v", collection, got)
		}
	}
}

func TestTimestampFieldPreservesScanLimits(t *testing.T) {
	_, path := timestampFixture(t, `{"occurred_at":"2026-10-21T00:00:00Z","order_id":"noise"}`+"\n"+`{"occurred_at":"2026-10-22T00:00:00Z","order_id":"noise"}`+"\n", "occurred_at")
	for _, tc := range []struct {
		name      string
		configure func(*Config)
	}{
		{"entries", func(cfg *Config) { cfg.MaxEntries = 1 }},
		{"line", func(cfg *Config) { cfg.MaxLineBytes = 8 }},
		{"file", func(cfg *Config) { cfg.MaxFileBytes = 8 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{ID: "limited", Root: filepath.Dir(path), Sources: []Source{{Collection: "events", Path: filepath.Base(path), Format: JSONL, TimestampField: "occurred_at"}}}
			tc.configure(&cfg)
			db, err := NewDB(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer db.(interface{ Close() error }).Close()
			if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query(dal.WhereField("order_id", dal.Equal, "11045"))); err == nil {
				t.Fatal("mapping/filter bypassed scanned bound")
			}
		})
	}
}

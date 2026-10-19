package cloudlogging

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging"
	"github.com/dal-go/dalgo/dal"
	"google.golang.org/api/iterator"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
)

type fakeReader struct {
	entries []*logging.Entry
	index   int
}

type nextReader func() (*logging.Entry, error)

func (f nextReader) Next() (*logging.Entry, error) { return f() }

func (f *fakeReader) Next() (*logging.Entry, error) {
	if f.index >= len(f.entries) {
		return nil, iterator.Done
	}
	entry := f.entries[f.index]
	f.index++
	return entry, nil
}

func boundedQuery() dal.StructuredQuery {
	return dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").
		WhereField("order_id", dal.Equal, "11039").
		OrderBy(dal.DescendingField("timestamp")).Limit(10).SelectIntoRecord(nil)
}

func TestFilterIsBoundedAndEscaped(t *testing.T) {
	cfg := Config{Collection: "entries", MaxResults: 100, MaxWindow: 24 * time.Hour}
	q := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").
		WhereField("order_id", dal.Equal, `11039" OR severity>=DEFAULT`).
		OrderBy(dal.DescendingField("timestamp")).Limit(20).SelectIntoRecord(nil)
	filter, limit, err := buildFilter(q, cfg)
	if err != nil || limit != 20 {
		t.Fatalf("filter: %q, %d, %v", filter, limit, err)
	}
	if !strings.Contains(filter, `jsonPayload.order_id="11039\" OR severity>=DEFAULT"`) {
		t.Fatalf("identifier was not quoted: %q", filter)
	}
	withTrace := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").
		WhereField("correlation_id", dal.Equal, "projects/demo/traces/abc").SelectIntoRecord(nil)
	traceFilter, _, err := buildFilter(withTrace, cfg)
	if err != nil || !strings.Contains(traceFilter, `trace="projects/demo/traces/abc"`) {
		t.Fatalf("trace filter = %q, %v", traceFilter, err)
	}
	bare := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().SelectIntoRecord(nil)
	if _, _, err := buildFilter(bare, cfg); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("unbounded query: %v", err)
	}
	tooWide := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-17T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").SelectIntoRecord(nil)
	if _, _, err := buildFilter(tooWide, cfg); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("wide query: %v", err)
	}
}

func TestMapEntryPreservesPayloadAndReference(t *testing.T) {
	e := &logging.Entry{Timestamp: time.Date(2026, 10, 19, 3, 0, 0, 0, time.UTC), Severity: logging.Error, InsertID: "abc", LogName: "projects/demo/logs/orders", Labels: map[string]string{"correlation_id": "corr-11039"}, Payload: map[string]any{"message": "carrier failed", "order_id": "11039"}}
	row, err := mapEntry(e, "demo-project")
	if err != nil {
		t.Fatal(err)
	}
	if row["order_id"] != "11039" || row["severity"] != "ERROR" || !strings.HasPrefix(row["source_ref"].(string), "gcl:") || row["correlation_id"] != "corr-11039" {
		t.Fatalf("mapped row: %#v", row)
	}
	row2, err := mapEntry(e, "another-project")
	if err != nil {
		t.Fatal(err)
	}
	if row2["source_ref"] == row["source_ref"] {
		t.Fatal("source references collide across projects")
	}
	if !strings.Contains(row["raw"].(string), "carrier failed") {
		t.Fatalf("payload missing: %#v", row)
	}
}

func TestUnencodablePayloadDoesNotLeakContent(t *testing.T) {
	entry := &logging.Entry{Payload: make(chan int)}
	if _, err := mapEntry(entry, "demo-project"); err == nil || strings.Contains(err.Error(), "chan") {
		t.Fatalf("encoding error leaked source details: %v", err)
	}
}

func TestCloudQueryReadsCurrentEntriesWithoutCredentialOrStorage(t *testing.T) {
	entry := &logging.Entry{Timestamp: time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC), Severity: logging.Error, Payload: map[string]any{"message": "carrier failed", "order_id": "11039"}}
	var pushed string
	db := &database{cfg: Config{Collection: "entries", ProjectID: "owned-project", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Second}, entries: func(_ context.Context, filter string, limit int) entryReader {
		pushed = filter
		if limit != 10 {
			t.Fatalf("limit = %d", limit)
		}
		return &fakeReader{entries: []*logging.Entry{entry}}
	}}
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), boundedQuery())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	row, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	data := row.Data().(map[string]any)
	if data["order_id"] != "11039" || !strings.Contains(pushed, `jsonPayload.order_id="11039"`) || !strings.Contains(pushed, `timestamp >= "2026-10-19T03:00:00Z"`) {
		t.Fatalf("entry or pushed filter: %#v, %q", data, pushed)
	}
	if _, err := reader.Next(); err != dal.ErrNoMoreRecords {
		t.Fatalf("reader end: %v", err)
	}
	entry.Payload = strings.Repeat("x", maxEntryBytes)
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), boundedQuery()); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("oversized entry: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.ExecuteQueryToRecordsReader(ctx, boundedQuery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled query: %v", err)
	}
	if err := db.RunReadwriteTransaction(context.Background(), func(context.Context, dal.ReadwriteTransaction) error { t.Fatal("write worker ran"); return nil }); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("write: %v", err)
	}
}

func TestCloudQueryBoundsCompleteMappedRows(t *testing.T) {
	entry := &logging.Entry{Timestamp: time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC), Payload: map[string]any{"message": strings.Repeat("x", 400_000)}}
	// This test intentionally serializes many large records to exercise the byte
	// budget. Give that work ample time under race instrumentation so the query
	// timeout cannot mask the byte-limit assertion.
	db := &database{cfg: Config{Collection: "entries", ProjectID: "owned-project", MaxResults: 30, MaxWindow: time.Hour, MaxQueryDuration: 5 * time.Minute}, entries: func(context.Context, string, int) entryReader {
		return &fakeReader{entries: []*logging.Entry{entry}}
	}}
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), boundedQuery()); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("duplicated mapped fields exceeded entry budget: %v", err)
	}
	entry.Payload = map[string]any{"message": strings.Repeat("x", 290_000)}
	entries := make([]*logging.Entry, 20)
	for i := range entries {
		entries[i] = entry
	}
	db.entries = func(context.Context, string, int) entryReader { return &fakeReader{entries: entries} }
	q := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").Limit(20).SelectIntoRecord(nil)
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), q); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("cumulative mapped rows exceeded result budget: %v", err)
	}
}

func TestHostedByteBudgets(t *testing.T) {
	entry := &logging.Entry{Timestamp: time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC), Payload: map[string]any{"message": strings.Repeat("x", 100_000), "order_id": "11039"}}
	entries := make([]*logging.Entry, 10)
	for i := range entries {
		entries[i] = entry
	}
	base := Config{ID: "logs", Collection: "entries", ProjectID: "demo-project", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Minute}
	for _, tc := range []struct {
		name string
		cfg  Config
		kind LimitKind
	}{
		{"hosted 2MiB scan cap", func() Config { c := base; c.MaxScanBytes = 2 << 20; return c }(), LimitScanBytes},
		{"per entry cap", func() Config { c := base; c.MaxEntryBytes = 4096; return c }(), LimitEntryBytes},
		{"result cap", func() Config { c := base; c.MaxResultBytes = 400_000; return c }(), LimitResultBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := normalizeConfig(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			db := &database{cfg: cfg, entries: func(context.Context, string, int) entryReader { return &fakeReader{entries: entries} }}
			_, err = db.ExecuteQueryToRecordsReader(context.Background(), boundedQuery())
			var limit *LimitError
			if !errors.Is(err, ErrLimitExceeded) || !errors.Is(err, dal.ErrNotSupported) || !errors.As(err, &limit) || limit.Kind != tc.kind {
				t.Fatalf("limit error = %v, want %s", err, tc.kind)
			}
		})
	}
}

func TestHostedScanCountsLocalMisses(t *testing.T) {
	entry := &logging.Entry{Timestamp: time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC), Labels: map[string]string{"order_id": "11039"}, Payload: map[string]any{"message": strings.Repeat("x", 100_000), "order_id": "different"}}
	entries := make([]*logging.Entry, 10)
	for i := range entries {
		entries[i] = entry
	}
	cfg, err := normalizeConfig(Config{ID: "logs", Collection: "entries", ProjectID: "demo-project", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Minute, MaxScanBytes: 2 << 20, MaxResultBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	db := &database{cfg: cfg, entries: func(context.Context, string, int) entryReader { return &fakeReader{entries: entries} }}
	_, err = db.ExecuteQueryToRecordsReader(context.Background(), boundedQuery())
	var limit *LimitError
	if !errors.As(err, &limit) || limit.Kind != LimitScanBytes {
		t.Fatalf("local misses should exhaust scan budget: %v", err)
	}
}

func TestHostedBudgetConfigurationRejectsInvalidValues(t *testing.T) {
	base := Config{ID: "logs", Collection: "entries", ProjectID: "demo-project"}
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"negative entry", func(c *Config) { c.MaxEntryBytes = -1 }},
		{"entry ceiling", func(c *Config) { c.MaxEntryBytes = maxEntryBytes + 1 }},
		{"negative scan", func(c *Config) { c.MaxScanBytes = -1 }},
		{"scan ceiling", func(c *Config) { c.MaxScanBytes = maxResultBytes + 1 }},
		{"negative result", func(c *Config) { c.MaxResultBytes = -1 }},
		{"result ceiling", func(c *Config) { c.MaxResultBytes = maxResultBytes + 1 }},
		{"negative pages", func(c *Config) { c.MaxPages = -1 }},
		{"page ceiling", func(c *Config) { c.MaxPages = maxPersonalPages + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.change(&cfg)
			if _, err := normalizeConfig(cfg); err == nil {
				t.Fatal("invalid budget accepted")
			}
		})
	}
	cfg, err := normalizeConfig(base)
	if err != nil || cfg.MaxEntryBytes != maxEntryBytes || cfg.MaxScanBytes != maxResultBytes || cfg.MaxResultBytes != maxResultBytes || cfg.MaxPages != maxPersonalPages {
		t.Fatalf("default budgets = %#v, %v", cfg, err)
	}
}

func TestCloudQueryCancellationAfterProviderRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := &database{cfg: Config{Collection: "entries", ProjectID: "demo-project", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Minute}, entries: func(context.Context, string, int) entryReader {
		return nextReader(func() (*logging.Entry, error) {
			cancel()
			return nil, iterator.Done
		})
	}}
	if _, err := db.ExecuteQueryToRecordsReader(ctx, boundedQuery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation after provider read = %v", err)
	}
}

func TestCloudQueryHonorsMappedAliasPrecedenceBeforeLimit(t *testing.T) {
	withinWindow := time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC)
	tests := []struct {
		field   string
		entries []*logging.Entry
	}{
		{field: "order_id", entries: []*logging.Entry{
			{Timestamp: withinWindow, Labels: map[string]string{"order_id": "11039"}, Payload: map[string]any{"message": "conflicting alias", "order_id": "different"}},
			{Timestamp: withinWindow, Labels: map[string]string{"order_id": "11039"}, Payload: map[string]any{"message": "matching payload", "order_id": "11039"}},
		}},
		{field: "correlation_id", entries: []*logging.Entry{
			{Timestamp: withinWindow, Trace: "11039", Labels: map[string]string{"correlation_id": "11039"}, Payload: map[string]any{"message": "conflicting alias", "correlation_id": "different"}},
			{Timestamp: withinWindow, Trace: "different", Labels: map[string]string{"correlation_id": "different"}, Payload: map[string]any{"message": "matching payload", "correlation_id": "11039"}},
		}},
		{field: "service", entries: []*logging.Entry{
			{Timestamp: withinWindow, Resource: &mrpb.MonitoredResource{Labels: map[string]string{"service_name": "11039"}}, Labels: map[string]string{"service": "11039"}, Payload: map[string]any{"message": "conflicting alias", "service": "different"}},
			{Timestamp: withinWindow, Resource: &mrpb.MonitoredResource{Labels: map[string]string{"service_name": "different"}}, Labels: map[string]string{"service": "different"}, Payload: map[string]any{"message": "matching payload", "service": "11039"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			db := &database{cfg: Config{Collection: "entries", ProjectID: "owned-project", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Second}, entries: func(context.Context, string, int) entryReader {
				return &fakeReader{entries: tc.entries}
			}}
			q := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
				WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
				WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").
				WhereField(tc.field, dal.Equal, "11039").Limit(1).SelectIntoRecord(nil)
			reader, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			row, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			data := row.Data().(map[string]any)
			if data["message"] != "matching payload" || data[tc.field] != "11039" {
				t.Fatalf("query returned a row whose mapped value does not match the predicate: %#v", data)
			}
			if _, err := reader.Next(); err != dal.ErrNoMoreRecords {
				t.Fatalf("result limit = %v", err)
			}
		})
	}
}

func TestCloudQueryRejectsNonASCIIIdentifierPredicate(t *testing.T) {
	query := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").
		WhereField("order_id", dal.Equal, "café").Limit(1).SelectIntoRecord(nil)
	if _, _, err := buildFilter(query, Config{Collection: "entries", MaxResults: 20, MaxWindow: time.Hour}); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("non-ASCII identifier predicate = %v", err)
	}
}

func TestCloudQueryReportsUnsupportedForNonASCIICandidate(t *testing.T) {
	entry := &logging.Entry{
		Timestamp: time.Date(2026, 10, 19, 3, 30, 0, 0, time.UTC),
		Payload:   map[string]any{"message": "compatibility-form identifier", "order_id": "Ａ-11039"},
	}
	db := &database{cfg: Config{Collection: "entries", ProjectID: "owned-project", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Second}, entries: func(context.Context, string, int) entryReader {
		return &fakeReader{entries: []*logging.Entry{entry}}
	}}
	query := dal.From(dal.NewRootCollectionRef("entries", "")).NewQuery().
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-19T03:00:00Z").
		WhereField("timestamp", dal.LessOrEqual, "2026-10-19T04:00:00Z").
		WhereField("order_id", dal.Equal, "A-11039").Limit(1).SelectIntoRecord(nil)
	if _, err := db.ExecuteQueryToRecordsReader(context.Background(), query); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("non-ASCII provider candidate was silently dropped instead of rejected: %v", err)
	}
}

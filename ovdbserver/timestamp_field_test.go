package ovdbserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2logs"
	"github.com/dal-go/dalgo2openvaultdb"
	"github.com/dal-go/record"
)

type timestampHandlerTransport struct{ handler http.Handler }

func (transport timestampHandlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// Every request is served in-process; no network fallback or listener exists.
	if request.URL.Scheme != "https" || request.URL.Host != "direct-log.test" {
		return nil, fmt.Errorf("unexpected direct test origin")
	}
	response := httptest.NewRecorder()
	transport.handler.ServeHTTP(response, request)
	result := response.Result()
	result.Request = request
	return result, nil
}

type timestampEvidenceRow struct {
	Timestamp  string `json:"timestamp"`
	Raw        string `json:"raw"`
	FieldsJSON string `json:"fields_json"`
	SourceRef  string `json:"source_ref"`
	Parser     string `json:"parser"`
	LineStart  int    `json:"line_start"`
	LineEnd    int    `json:"line_end"`
	ByteOffset int64  `json:"byte_offset"`
}

func TestTimestampFieldUnmodifiedOVDBClientReadsActualFileWithoutListener(t *testing.T) {
	root := t.TempDir()
	raw := `{"occurred_at":"2026-10-21T10:30:00.123456789+02:00","timestamp":"2099-01-01T00:00:00Z","order_id":"11045","message":"shipping request","extra":9007199254740993}` + "\n"
	if err := os.WriteFile(filepath.Join(root, "shipping.jsonl"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := New(context.Background(), Config{Files: []dalgo2logs.Config{{ID: "shipping", Root: root, Sources: []dalgo2logs.Source{{Collection: "events", Path: "shipping.jsonl", Format: dalgo2logs.JSONL, TimestampField: "occurred_at"}}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	httpClient := &http.Client{Transport: timestampHandlerTransport{handler: service.Handler}}
	response, err := httpClient.Get("https://direct-log.test/v1/databases/shipping")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var descriptor struct {
		Collections  []string        `json:"collections"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&descriptor) != nil || len(descriptor.Collections) != 1 || descriptor.Collections[0] != "events" || !descriptor.Capabilities["query"] || descriptor.Capabilities["write"] {
		t.Fatalf("ordinary discovery changed: %#v status=%d", descriptor, response.StatusCode)
	}
	client, err := dalgo2openvaultdb.NewDB("https://direct-log.test", "shipping", dalgo2openvaultdb.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	query := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().
		WhereField("order_id", dal.Equal, "11045").
		WhereField("timestamp", dal.GreaterOrEqual, "2026-10-21T08:30:00Z").
		WhereField("timestamp", dal.LessThen, "2026-10-21T08:30:00.2Z").
		OrderBy(dal.AscendingField("timestamp")).Limit(1).
		SelectIntoRecord(func() record.Record {
			return record.NewRecordWithData(record.NewKeyWithID("events", ""), &timestampEvidenceRow{})
		})
	reader, err := client.ExecuteQueryToRecordsReader(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	rec, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	got := rec.Data().(*timestampEvidenceRow)
	digest := sha256.Sum256([]byte(raw))
	wantRef := fmt.Sprintf("shipping:events:%s:0:%d", hex.EncodeToString(digest[:]), len(raw))
	if got.Timestamp != "2026-10-21T08:30:00.123456789Z" || got.Raw != raw || got.SourceRef != wantRef || got.Parser != "jsonl" || got.LineStart != 1 || got.LineEnd != 1 || got.ByteOffset != 0 {
		t.Fatalf("actual OVDB client changed normalization/evidence: %+v", got)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &original); err != nil {
		t.Fatal(err)
	}
	wantFields, err := json.Marshal(original)
	if err != nil || got.FieldsJSON != string(wantFields) {
		t.Fatalf("original fields changed in OVDB: %v %s", err, got.FieldsJSON)
	}
	if _, err := reader.Next(); err != dal.ErrNoMoreRecords {
		t.Fatalf("expected one bounded original row, got %v", err)
	}
}

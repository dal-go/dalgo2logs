package ovdbserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2logs"
	"github.com/dal-go/dalgo2logs/cloudlogging"
	"github.com/dal-go/dalgo2openvaultdb"
	"github.com/dal-go/record"
)

type evidenceRow struct {
	Message   string `json:"message"`
	SourceRef string `json:"source_ref"`
}

func TestUnmodifiedOVDBClientDiscoversAndQueriesDirectFile(t *testing.T) {
	root := t.TempDir()
	content := []byte("{\"timestamp\":\"2026-10-19T03:00:00Z\",\"severity\":\"ERROR\",\"order_id\":\"11039\",\"message\":\"carrier failed\"}\n" +
		"{\"timestamp\":\"2026-10-19T03:01:00Z\",\"severity\":\"INFO\",\"order_id\":\"11040\",\"message\":\"noise\"}\n")
	if err := os.WriteFile(filepath.Join(root, "orders.jsonl"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), Config{Files: []dalgo2logs.Config{{ID: "evidence", Root: root, Sources: []dalgo2logs.Source{{Collection: "events", Path: "orders.jsonl", Format: dalgo2logs.JSONL}}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := svc.Close(); err != nil {
			t.Error(err)
		}
	}()
	httpSrv := httptest.NewServer(svc.Handler)
	defer httpSrv.Close()
	resp, err := http.Get(httpSrv.URL + "/v1/databases/evidence")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery status: %d", resp.StatusCode)
	}
	var metadata struct {
		Collections  []string        `json:"collections"`
		Capabilities map[string]bool `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if len(metadata.Collections) != 1 || metadata.Collections[0] != "events" || !metadata.Capabilities["query"] || metadata.Capabilities["write"] {
		t.Fatalf("ordinary OVDB metadata: %#v", metadata)
	}
	client, err := dalgo2openvaultdb.NewDB(httpSrv.URL, "evidence")
	if err != nil {
		t.Fatal(err)
	}
	q := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().WhereField("order_id", dal.Equal, "11039").Limit(10).SelectIntoRecord(func() record.Record {
		return record.NewRecordWithData(record.NewKeyWithID("events", ""), &evidenceRow{})
	})
	reader, err := client.ExecuteQueryToRecordsReader(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reader.Close(); err != nil {
			t.Error(err)
		}
	}()
	rec, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	row := rec.Data().(*evidenceRow)
	if row.Message != "carrier failed" || row.SourceRef == "" {
		t.Fatalf("query result: %#v", row)
	}
	if _, err := reader.Next(); err != dal.ErrNoMoreRecords {
		t.Fatalf("expected one result, got %v", err)
	}
}

func TestCloudMountRequiresOwnerToken(t *testing.T) {
	_, err := New(context.Background(), Config{Cloud: []cloudlogging.Config{{ID: "cloud", ProjectID: "example-project", Collection: "entries"}}}, "")
	if err == nil {
		t.Fatal("unauthenticated Cloud source accepted")
	}
}

func TestOwnerTokenProtectsDirectSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc, err := New(context.Background(), Config{Files: []dalgo2logs.Config{{ID: "evidence", Root: root, Sources: []dalgo2logs.Source{{Collection: "events", Path: "evidence.jsonl", Format: dalgo2logs.JSONL}}}}}, "private-token")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := svc.Close(); err != nil {
			t.Error(err)
		}
	}()
	server := httptest.NewServer(svc.Handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/v1/databases/evidence", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without token: %d", response.StatusCode)
	}
	request.Header.Set("Authorization", "Bearer private-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Error(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("with token: %d", response.StatusCode)
	}
}

func TestCORSRequiresExactOrigin(t *testing.T) {
	_, err := New(context.Background(), Config{
		Files:       []dalgo2logs.Config{{ID: "evidence", Root: t.TempDir()}},
		CORSOrigins: []string{"*"},
	}, "")
	if err == nil || !strings.Contains(err.Error(), "CORS") {
		t.Fatalf("wildcard CORS origin accepted: %v", err)
	}
}

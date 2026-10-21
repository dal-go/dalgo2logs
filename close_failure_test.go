package dalgo2logs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo/dal"
)

func TestDALQueryRejectsEvidenceWhenSourceCloseFails(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "shipping.jsonl")
	if err := os.WriteFile(path, []byte(`{"occurred_at":"2026-10-21T00:00:00Z","message":"evidence"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ownedRoot, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ownedRoot.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, failScan := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed scan", true: "scan already failed"}[failScan], func(t *testing.T) {
			cfg := Config{ID: "shipping", Root: root, MaxFileBytes: 4096, MaxLineBytes: 1024, MaxEntries: 10, MaxResults: 10}
			if failScan {
				cfg.MaxLineBytes = 1
			}
			closeCalls := 0
			backend := &database{cfg: cfg, root: ownedRoot, sources: map[string]source{"events": {Source: Source{Collection: "events", Path: "shipping.jsonl", Format: JSONL, TimestampField: "occurred_at"}, path: path}}, closeSource: func(file *os.File) error {
				closeCalls++
				return errors.Join(file.Close(), errors.New("private close failure details"))
			}}
			db := dal.NewDB(backend)
			q := dal.From(dal.NewRootCollectionRef("events", "")).NewQuery().SelectIntoRecord(nil)
			reader, err := db.ExecuteQueryToRecordsReader(context.Background(), q)
			if reader != nil || err == nil || !strings.Contains(err.Error(), "source close failed") || strings.Contains(err.Error(), "private") || closeCalls != 1 {
				t.Fatalf("failed cleanup granted evidence or leaked details: reader=%v error=%v closes=%d", reader, err, closeCalls)
			}
			if failScan && !strings.Contains(err.Error(), "line limit exceeded") {
				t.Fatalf("cleanup masked the original scan failure: %v", err)
			}
		})
	}
}

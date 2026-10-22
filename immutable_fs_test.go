package dalgo2logs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

func immutableConfig() Config {
	return Config{ID: "shipping", Sources: []Source{{Collection: "events", Path: "logs/shipping.jsonl", Format: JSONL, TimestampField: "occurred_at"}}}
}
func immutableFixture(t *testing.T, cfg Config, files fs.FS) dal.DB {
	t.Helper()
	db, err := NewDBFromImmutableFS(cfg, files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.(interface{ Close() error }).Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}
func TestImmutableFSExactEvidenceAndOSParity(t *testing.T) {
	first := `{"occurred_at":"2026-10-21T00:00:00Z","order_id":9223372036854775807,"message":"observed"}` + "\r\n"
	content := first + "{\"message\":\"untimed\"}\nnot json\r\n{\"occurred_at\":\"bad\"}\n"
	files := fstest.MapFS{"logs/shipping.jsonl": {Data: []byte(content)}}
	db := immutableFixture(t, immutableConfig(), files)
	all := rows(t, db, query())
	if len(all) != 4 {
		t.Fatalf("entries=%d", len(all))
	}
	osDB, _ := timestampFixture(t, content, "occurred_at")
	original := rows(t, osDB, query())
	for i, rec := range all {
		if rec.Key().ID != original[i].Key().ID || !reflect.DeepEqual(rec.Data(), original[i].Data()) {
			t.Fatalf("FS/OS evidence differs: %#v %#v", rec.Data(), original[i].Data())
		}
	}
	digest := sha256.Sum256([]byte(content))
	ref := fmt.Sprintf("shipping:events:%s:0:%d", hex.EncodeToString(digest[:]), len(first))
	got := rows(t, db, query(dal.WhereField("source_ref", dal.Equal, ref)))
	if len(got) != 1 {
		t.Fatalf("exact span returned %d rows", len(got))
	}
	data := got[0].Data().(map[string]any)
	if data["raw"] != first || data["byte_offset"] != int64(0) || data["timestamp"] != "2026-10-21T00:00:00Z" || data["order_id"] != "9223372036854775807" || data["parser"] != "jsonl" || data["parse_status"] != "parsed" || !strings.Contains(data["fields_json"].(string), "9223372036854775807") {
		t.Fatalf("lost original evidence: %#v", data)
	}
	reopened := record.NewRecordWithData(got[0].Key(), map[string]any{})
	if err := db.Get(context.Background(), reopened); err != nil || !reflect.DeepEqual(reopened.Data(), data) {
		t.Fatalf("keyed reread changed: %v %#v", err, reopened.Data())
	}
	for _, badRef := range []string{ref + "0", fmt.Sprintf("shipping:events:%s:1:%d", hex.EncodeToString(digest[:]), len(first)), strings.Replace(ref, ":events:", ":other:", 1)} {
		if got := rows(t, db, query(dal.WhereField("source_ref", dal.Equal, badRef))); len(got) != 0 {
			t.Fatalf("forged span/artifact matched %q", badRef)
		}
	}
	states := map[string]bool{}
	for _, rec := range all {
		states[rec.Data().(map[string]any)["parse_status"].(string)] = true
	}
	for _, status := range []string{"parsed", "missing_timestamp", "malformed", "invalid_timestamp"} {
		if !states[status] {
			t.Errorf("missing parse state %s", status)
		}
	}
	if err := db.(interface{ Close() error }).Close(); err != nil {
		t.Fatal(err)
	}
	if reader, err := db.ExecuteQueryToRecordsReader(context.Background(), query()); reader != nil || err == nil {
		t.Fatal("closed DB returned evidence")
	}
	// The borrowed filesystem remains usable after closing the DB.
	if _, err := fs.ReadFile(files, "logs/shipping.jsonl"); err != nil {
		t.Fatal(err)
	}
}

func TestImmutableFSConfiguration(t *testing.T) {
	files := fstest.MapFS{"logs/shipping.jsonl": {Data: []byte("{}\n")}, "directory": {Mode: fs.ModeDir}, "link": {Mode: fs.ModeSymlink, Data: []byte("logs/shipping.jsonl")}, "dir-link": {Mode: fs.ModeSymlink, Data: []byte("logs")}}
	for _, path := range []string{"../outside", "/absolute", "logs/../shipping.jsonl", "logs//shipping.jsonl", "logs\\shipping.jsonl", ".", "missing", "directory", "link", "dir-link/shipping.jsonl"} {
		t.Run(path, func(t *testing.T) {
			cfg := immutableConfig()
			cfg.Sources[0].Path = path
			if db, err := NewDBFromImmutableFS(cfg, files); db != nil || err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	cfg := immutableConfig()
	cfg.Root = "."
	if _, err := NewDBFromImmutableFS(cfg, files); err == nil {
		t.Fatal("ambiguous OS/FS mode accepted")
	}
	if _, err := NewDBFromImmutableFS(immutableConfig(), nil); err == nil {
		t.Fatal("nil FS accepted")
	}
	if _, err := NewDB(immutableConfig()); err == nil {
		t.Fatal("OS constructor accepted missing root")
	}
}

type faultFS struct {
	fs.FS
	openErr, readErr, closeErr bool
	statFailure                int
	cancel                     context.CancelFunc
	closes                     int
}

func (f *faultFS) Open(name string) (fs.File, error) {
	if f.openErr {
		return nil, errors.New("private open failure")
	}
	file, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultFile{File: file, owner: f}, nil
}

type faultFile struct {
	fs.File
	owner *faultFS
	stats int
}

func (f *faultFile) Read(p []byte) (int, error) {
	if f.owner.cancel != nil {
		f.owner.cancel()
	}
	if f.owner.readErr {
		return 0, errors.New("private read failure")
	}
	return f.File.Read(p)
}
func (f *faultFile) Stat() (fs.FileInfo, error) {
	f.stats++
	if f.stats == f.owner.statFailure {
		return nil, errors.New("private stat failure")
	}
	return f.File.Stat()
}
func (f *faultFile) Close() error {
	f.owner.closes++
	err := f.File.Close()
	if f.owner.closeErr {
		return errors.Join(err, errors.New("private close failure"))
	}
	return err
}
func TestImmutableFSFailuresNeverGrantEvidence(t *testing.T) {
	for _, failure := range []string{"open", "read", "close", "read and close", "first stat", "last stat", "canceled read"} {
		t.Run(failure, func(t *testing.T) {
			files := &faultFS{FS: fstest.MapFS{"logs/shipping.jsonl": {Data: []byte("{}\n{}\n")}}}
			db := immutableFixture(t, immutableConfig(), files)
			if files.closes != 1 {
				t.Fatal("constructor did not close validation handle")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch failure {
			case "open":
				files.openErr = true
			case "read":
				files.readErr = true
			case "close":
				files.closeErr = true
			case "read and close":
				files.readErr = true
				files.closeErr = true
			case "first stat":
				files.statFailure = 1
			case "last stat":
				files.statFailure = 2
			case "canceled read":
				files.cancel = cancel
			}
			reader, err := db.ExecuteQueryToRecordsReader(ctx, query())
			if reader != nil || err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("failed source returned evidence/private error: %v %v", reader, err)
			}
			if failure != "open" && files.closes != 2 {
				t.Fatalf("scan handle not closed: %d", files.closes)
			}
			if failure == "read and close" && (!strings.Contains(err.Error(), "file read failed") || !strings.Contains(err.Error(), "source close failed")) {
				t.Fatalf("close masked read failure: %v", err)
			}
			if failure == "canceled read" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
		})
	}
	for _, failure := range []string{"open", "stat", "close"} {
		t.Run("constructor "+failure, func(t *testing.T) {
			files := &faultFS{FS: fstest.MapFS{"logs/shipping.jsonl": {Data: []byte("{}\n")}}}
			switch failure {
			case "open":
				files.openErr = true
			case "stat":
				files.statFailure = 1
			case "close":
				files.closeErr = true
			}
			if db, err := NewDBFromImmutableFS(immutableConfig(), files); db != nil || err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("failed construction accepted: %v %v", db, err)
			}
		})
	}
}
func TestImmutableFSScanLimitsAndPreCancellation(t *testing.T) {
	for _, bound := range []string{"file", "line", "entries"} {
		t.Run(bound, func(t *testing.T) {
			cfg := immutableConfig()
			switch bound {
			case "file":
				cfg.MaxFileBytes = 2
			case "line":
				cfg.MaxLineBytes = 1
			case "entries":
				cfg.MaxEntries = 1
			}
			db := immutableFixture(t, cfg, fstest.MapFS{"logs/shipping.jsonl": {Data: []byte("{}\n{}\n")}})
			reader, err := db.ExecuteQueryToRecordsReader(context.Background(), query(dal.WhereField("order_id", dal.Equal, "absent")))
			if reader != nil || err == nil {
				t.Fatalf("%s limit bypassed by filter", bound)
			}
		})
	}
	db := immutableFixture(t, immutableConfig(), fstest.MapFS{"logs/shipping.jsonl": {Data: []byte("{}\n")}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if reader, err := db.ExecuteQueryToRecordsReader(ctx, query()); reader != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v %v", reader, err)
	}
}

func TestImmutableFSTextUsesExistingParser(t *testing.T) {
	content := "2026-10-21T00:00:00Z ERROR shipping order_id=11045 failed\r\n  original stack detail\r\n"
	cfg := immutableConfig()
	cfg.Sources[0].Format = Text
	cfg.Sources[0].TimestampField = ""
	db := immutableFixture(t, cfg, fstest.MapFS{"logs/shipping.jsonl": {Data: []byte(content)}})
	got := rows(t, db, query())
	if len(got) != 1 {
		t.Fatalf("multiline framing changed: %d", len(got))
	}
	data := got[0].Data().(map[string]any)
	if data["raw"] != content || data["line_start"] != 1 || data["line_end"] != 2 || data["parser"] != "timestamped-text" || data["parse_status"] != "parsed" || !strings.Contains(data["message"].(string), "original stack detail") {
		t.Fatalf("existing text parser changed: %#v", data)
	}
}

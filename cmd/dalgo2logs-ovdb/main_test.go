package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dal-go/dalgo2logs"
	"github.com/dal-go/dalgo2logs/ovdbserver"
)

type failingConfigClose struct {
	*os.File
	closeErr error
	calls    *int
}

func (f failingConfigClose) Close() error { *f.calls++; return errors.Join(f.File.Close(), f.closeErr) }

func TestRunUsesProductionConfigOpenerWithoutStartingListener(t *testing.T) {
	if err := run(""); err == nil {
		t.Fatal("empty config accepted")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen":"0.0.0.0:6832"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run(path); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("operator file was not read/validated: %v", err)
	}
}

func TestRunPropagatesOwnedCleanupFailuresBeforeListenerExists(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "events.jsonl"), []byte(`{"occurred_at":"2026-10-21T00:00:00Z"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := fileConfig{Listen: "127.0.0.1:6832", Sources: ovdbserver.Config{Files: []dalgo2logs.Config{{ID: "shipping", Root: root, Sources: []dalgo2logs.Source{{Collection: "events", Path: "events.jsonl", Format: dalgo2logs.JSONL, TimestampField: "occurred_at"}}}}}}
	content, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	bindErr := errors.New("test bind failure")
	serviceErr := errors.New("test service close failure")
	for _, failCleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal cleanup", true: "cleanup failures"}[failCleanup], func(t *testing.T) {
			configCloses, serviceCloses, listens := 0, 0, 0
			var configErr, errorService error
			if failCleanup {
				configErr = errors.New("private config close details")
				errorService = serviceErr
			}
			owned := commandIO{
				openConfig: func(name string) (io.ReadCloser, error) {
					if name != path {
						t.Fatalf("config path changed: %q", name)
					}
					file, err := os.Open(name)
					if err != nil {
						return nil, err
					}
					return failingConfigClose{File: file, closeErr: configErr, calls: &configCloses}, nil
				},
				closeService: func(service *ovdbserver.Service) error {
					serviceCloses++
					// The real composed service owns its opened file root; release it before
					// injecting the close error so this test cannot hide a resource leak.
					return errors.Join(service.Close(), errorService)
				},
				listen: func(network, address string) (net.Listener, error) {
					listens++
					if network != "tcp" || address != cfg.Listen {
						t.Fatalf("listener configuration changed: %s %s", network, address)
					}
					return nil, bindErr
				},
			}
			err := runWithIO(path, owned)
			if !errors.Is(err, bindErr) || configCloses != 1 || serviceCloses != 1 || listens != 1 {
				t.Fatalf("owning invocation failed cleanup: error=%v config=%d service=%d listen=%d", err, configCloses, serviceCloses, listens)
			}
			if failCleanup && (!errors.Is(err, serviceErr) || !strings.Contains(err.Error(), "configuration close failed") || strings.Contains(err.Error(), "private")) {
				t.Fatalf("cleanup failure masked or leaked: %v", err)
			}
			if !failCleanup && (errors.Is(err, serviceErr) || strings.Contains(err.Error(), "configuration close failed")) {
				t.Fatalf("successful cleanup invented a failure: %v", err)
			}
		})
	}
}

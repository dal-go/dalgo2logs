// Package ovdbserver composes direct DALGO sources with OpenVaultDB's existing
// HTTP server. It adds no log-specific code to OpenVaultDB core or protocol.
package ovdbserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo2logs"
	"github.com/dal-go/dalgo2logs/cloudlogging"
	"github.com/openvaultdb/openvaultdb-go/pkg/auth"
	"github.com/openvaultdb/openvaultdb-go/pkg/core"
	"github.com/openvaultdb/openvaultdb-go/pkg/manifest"
	"github.com/openvaultdb/openvaultdb-go/pkg/schema"
	"github.com/openvaultdb/openvaultdb-go/pkg/server"
)

// Config is trusted server-side source configuration. No source or project
// identifier is accepted from an HTTP request.
type Config struct {
	Files       []dalgo2logs.Config   `json:"files"`
	Cloud       []cloudlogging.Config `json:"cloud"`
	CORSOrigins []string              `json:"corsOrigins"`
}

type Service struct {
	Handler http.Handler
	server  *server.Server
	mounts  []*core.Database
}

// New opens each source directly, without ingestion or snapshotting. A Cloud
// source requires an owner token; deployments need further per-user policy at
// the gateway before exposing this server to multiple users.
func New(ctx context.Context, cfg Config, ownerToken string) (_ *Service, err error) {
	if len(cfg.Files)+len(cfg.Cloud) == 0 {
		return nil, errors.New("ovdbserver: at least one source is required")
	}
	if len(cfg.Cloud) > 0 && ownerToken == "" {
		return nil, errors.New("ovdbserver: Cloud Logging requires an owner token")
	}
	for _, raw := range cfg.CORSOrigins {
		parsed, parseErr := url.Parse(raw)
		if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(raw, "*") {
			return nil, errors.New("ovdbserver: CORS origins must be exact HTTP(S) origins")
		}
	}
	result := &Service{}
	defer func() {
		if err != nil {
			_ = result.Close()
		}
	}()
	mounts := make(map[string]*core.Database, len(cfg.Files)+len(cfg.Cloud))
	for _, source := range cfg.Files {
		db, openErr := dalgo2logs.NewDB(source)
		if openErr != nil {
			return nil, openErr
		}
		collections := make([]string, 0, len(source.Sources))
		for _, item := range source.Sources {
			collections = append(collections, item.Collection)
		}
		if err = result.addMount(mounts, source.ID, db, collections); err != nil {
			if closer, ok := db.(io.Closer); ok {
				_ = closer.Close()
			}
			return nil, err
		}
	}
	for _, source := range cfg.Cloud {
		db, openErr := cloudlogging.NewDB(ctx, source)
		if openErr != nil {
			return nil, openErr
		}
		if err = result.addMount(mounts, source.ID, db, []string{source.Collection}); err != nil {
			if closer, ok := db.(io.Closer); ok {
				_ = closer.Close()
			}
			return nil, err
		}
	}
	opts := []server.Option{server.WithReadOnly(true)}
	if len(cfg.CORSOrigins) > 0 {
		opts = append(opts, server.WithCORS(server.ParseCORSOrigins(cfg.CORSOrigins)))
	}
	if ownerToken != "" {
		opts = append(opts, server.WithAuth(&auth.Config{OwnerToken: ownerToken}))
	}
	result.server, err = server.NewChecked("dalgo2logs", mounts, opts...)
	if err != nil {
		return nil, err
	}
	result.Handler = result.server.Handler()
	return result, nil
}

func (s *Service) addMount(mounts map[string]*core.Database, id string, db dal.DB, names []string) error {
	if _, exists := mounts[id]; exists {
		return fmt.Errorf("ovdbserver: duplicate database ID")
	}
	collections := make(map[string]schema.Collection, len(names))
	for _, name := range names {
		if name == "" {
			return errors.New("ovdbserver: empty collection name")
		}
		if _, exists := collections[name]; exists {
			return errors.New("ovdbserver: duplicate collection name")
		}
		collections[name] = schema.Collection{Fields: commonFields()}
	}
	m := &manifest.Manifest{
		Database: manifest.Database{ID: id, SchemaMode: schema.ModeStrict},
		Storage:  manifest.Storage{Engine: "external"},
		Schemas:  &schema.Schemas{Collections: collections},
	}
	mount, err := core.OpenExternal(m, db)
	if err != nil {
		return err
	}
	mounts[id] = mount
	s.mounts = append(s.mounts, mount)
	return nil
}

func commonFields() map[string]schema.Field {
	fields := make(map[string]schema.Field)
	for _, name := range []string{"timestamp", "severity", "message", "service", "correlation_id", "trace_id", "order_id", "source", "source_ref", "parser", "parse_status", "raw", "fields_json", "labels_json", "project_id", "log_name", "insert_id"} {
		fields[name] = schema.Field{Type: schema.TypeString}
	}
	for _, name := range []string{"line_start", "line_end", "byte_offset"} {
		fields[name] = schema.Field{Type: schema.TypeInteger}
	}
	return fields
}

// Close releases provider clients and temporary server resources after the
// HTTP listener has stopped. It is safe after a partially failed New.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	if s.server != nil {
		s.server.CloseSnapshots()
	}
	var errs []error
	for _, mount := range s.mounts {
		errs = append(errs, mount.Close())
	}
	return errors.Join(errs...)
}

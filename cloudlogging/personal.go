package cloudlogging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/logging"
	logpb "cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"google.golang.org/api/iterator"
	loggingv2 "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
)

// LogView identifies one project-local Cloud Logging view. Its fields are
// supplied by trusted server state, never by a DALGO query.
type LogView struct {
	ProjectID string
	Location  string
	Bucket    string
	View      string
}

const (
	maxPersonalPages    = 10
	maxPersonalPageSize = 100
)

var (
	logViewLocation = regexp.MustCompile(`^[a-z0-9-]+$`)
	logViewPart     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func (v LogView) resourceName(expectedProject string) (string, error) {
	if !projectID.MatchString(v.ProjectID) || v.ProjectID != expectedProject ||
		!logViewLocation.MatchString(v.Location) || !logViewPart.MatchString(v.Bucket) || !logViewPart.MatchString(v.View) {
		return "", errors.New("cloudlogging: invalid project-local log view")
	}
	return "projects/" + v.ProjectID + "/locations/" + v.Location + "/buckets/" + v.Bucket + "/views/" + v.View, nil
}

// NewPersonalDB builds a request-scoped, read-only DALGO database using the
// caller's trusted context-aware user token provider and one fixed project-local
// log view. Token(ctx) must use ctx for any refresh network requests.
// The caller owns the returned database and should close it after the request.
func NewPersonalDB(ctx context.Context, cfg Config, view LogView, tokenProvider auth.TokenProvider) (dal.DB, error) {
	return newPersonalDB(ctx, cfg, view, tokenProvider)
}

func newPersonalDB(ctx context.Context, cfg Config, view LogView, tokenProvider auth.TokenProvider, options ...option.ClientOption) (dal.DB, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	resource, err := view.resourceName(cfg.ProjectID)
	if err != nil {
		return nil, err
	}
	if tokenProvider == nil {
		return nil, errors.New("cloudlogging: trusted context-aware user token provider is required")
	}
	credentials := auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: tokenProvider})
	clientOptions := append([]option.ClientOption{option.WithAuthCredentials(credentials)}, options...)
	service, err := loggingv2.NewService(ctx, clientOptions...)
	if err != nil {
		return nil, fmt.Errorf("cloudlogging: create personal read client: %w", err)
	}
	backend := &database{cfg: cfg, entries: func(ctx context.Context, filter string, limit int) entryReader {
		pageSize := limit
		if pageSize > maxPersonalPageSize {
			pageSize = maxPersonalPageSize
		}
		return &personalEntryReader{ctx: ctx, list: func(request *loggingv2.ListLogEntriesRequest) *loggingv2.EntriesListCall {
			return service.Entries.List(request)
		}, request: &loggingv2.ListLogEntriesRequest{
			ResourceNames: []string{resource}, Filter: filter, OrderBy: "timestamp desc", PageSize: int64(pageSize),
		}}
	}}
	return &cloudHandle{DB: dal.NewDB(backend), SchemaReader: backend, closeFn: func() error { return nil }}, nil
}

type personalEntryReader struct {
	ctx        context.Context
	list       func(*loggingv2.ListLogEntriesRequest) *loggingv2.EntriesListCall
	request    *loggingv2.ListLogEntriesRequest
	pageToken  string
	pages      int
	entries    []*loggingv2.LogEntry
	entryIndex int
	done       bool
}

func (r *personalEntryReader) Next() (*logging.Entry, error) {
	entry, _, err := r.NextWithSource()
	return entry, err
}

func (r *personalEntryReader) NextWithSource() (*logging.Entry, []byte, error) {
	for {
		if r.entryIndex < len(r.entries) {
			entry := r.entries[r.entryIndex]
			r.entryIndex++
			mapped, err := convertPersonalEntry(entry)
			if err != nil {
				return nil, nil, err
			}
			// Re-encode the decoded API object to retain its complete canonical
			// REST shape, including fields absent from logging.Entry.
			source, err := json.Marshal(entry)
			if err != nil {
				return nil, nil, errors.New("cloudlogging: REST log entry cannot be encoded")
			}
			return mapped, source, nil
		}
		if r.done {
			return nil, nil, iterator.Done
		}
		if r.pages >= maxPersonalPages {
			return nil, nil, errors.New("cloudlogging: personal log query exceeded page budget")
		}
		request := *r.request
		request.PageToken = r.pageToken
		response, err := r.list(&request).Context(r.ctx).Do()
		if err != nil {
			return nil, nil, err
		}
		r.pages++
		r.pageToken = response.NextPageToken
		r.entries = response.Entries
		r.entryIndex = 0
		if r.pageToken == "" {
			r.done = true
		}
		// A successful empty page with a continuation token is not exhaustion.
		if len(r.entries) == 0 && r.done {
			return nil, nil, iterator.Done
		}
	}
}

func convertPersonalEntry(e *loggingv2.LogEntry) (*logging.Entry, error) {
	if e == nil {
		return nil, errors.New("cloudlogging: empty log entry")
	}
	entry := &logging.Entry{
		Severity:     logging.ParseSeverity(e.Severity),
		InsertID:     e.InsertId,
		LogName:      e.LogName,
		Trace:        e.Trace,
		SpanID:       e.SpanId,
		TraceSampled: e.TraceSampled,
		Labels:       e.Labels,
	}
	if e.Timestamp != "" {
		timestamp, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			return nil, errors.New("cloudlogging: invalid entry timestamp")
		}
		entry.Timestamp = timestamp
	}
	if e.Resource != nil {
		entry.Resource = &mrpb.MonitoredResource{Type: e.Resource.Type, Labels: e.Resource.Labels}
	}
	if e.HttpRequest != nil {
		requestURL, err := url.Parse(e.HttpRequest.RequestUrl)
		if err != nil {
			return nil, errors.New("cloudlogging: invalid HTTP request URL in log entry")
		}
		request := &http.Request{
			Method: e.HttpRequest.RequestMethod,
			URL:    requestURL,
			Proto:  e.HttpRequest.Protocol,
			Header: make(http.Header),
		}
		if e.HttpRequest.Referer != "" {
			request.Header.Set("Referer", e.HttpRequest.Referer)
		}
		if e.HttpRequest.UserAgent != "" {
			request.Header.Set("User-Agent", e.HttpRequest.UserAgent)
		}
		latency, err := time.ParseDuration(e.HttpRequest.Latency)
		if e.HttpRequest.Latency != "" && err != nil {
			return nil, errors.New("cloudlogging: invalid HTTP request latency in log entry")
		}
		entry.HTTPRequest = &logging.HTTPRequest{
			Request: request, RequestSize: e.HttpRequest.RequestSize,
			Status: int(e.HttpRequest.Status), ResponseSize: e.HttpRequest.ResponseSize,
			Latency: latency, LocalIP: e.HttpRequest.ServerIp, RemoteIP: e.HttpRequest.RemoteIp,
			CacheHit: e.HttpRequest.CacheHit, CacheValidatedWithOriginServer: e.HttpRequest.CacheValidatedWithOriginServer,
			CacheFillBytes: e.HttpRequest.CacheFillBytes, CacheLookup: e.HttpRequest.CacheLookup,
		}
	}
	if e.Operation != nil {
		entry.Operation = &logpb.LogEntryOperation{Id: e.Operation.Id, Producer: e.Operation.Producer, First: e.Operation.First, Last: e.Operation.Last}
	}
	if e.SourceLocation != nil {
		entry.SourceLocation = &logpb.LogEntrySourceLocation{File: e.SourceLocation.File, Line: e.SourceLocation.Line, Function: e.SourceLocation.Function}
	}
	payload := e.JsonPayload
	if len(payload) == 0 {
		payload = e.ProtoPayload
	}
	if len(payload) > 0 {
		var value any
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, errors.New("cloudlogging: invalid structured entry payload")
		}
		entry.Payload = value
	} else {
		entry.Payload = e.TextPayload
	}
	return entry, nil
}

var _ dbschema.SchemaReader = (*database)(nil)

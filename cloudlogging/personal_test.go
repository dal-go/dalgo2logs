package cloudlogging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/httptransport"
	"cloud.google.com/go/logging"
	"github.com/dal-go/dalgo/dal"
	"google.golang.org/api/iterator"
	loggingv2 "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
)

func validLogView() LogView {
	return LogView{ProjectID: "demo-project", Location: "global", Bucket: "_Default", View: "_AllLogs"}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type staticTokenProvider struct{ token *auth.Token }

func (p staticTokenProvider) Token(ctx context.Context) (*auth.Token, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.token, nil
}

func apiResponse(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestPersonalLogViewValidation(t *testing.T) {
	view := validLogView()
	resource, err := view.resourceName("demo-project")
	if err != nil || resource != "projects/demo-project/locations/global/buckets/_Default/views/_AllLogs" {
		t.Fatalf("resource = %q, %v", resource, err)
	}
	for _, tc := range []struct {
		name string
		view LogView
	}{
		{"different project", LogView{ProjectID: "other-project", Location: "global", Bucket: "_Default", View: "_AllLogs"}},
		{"path injection", LogView{ProjectID: "demo-project", Location: "global/buckets/evil", Bucket: "_Default", View: "_AllLogs"}},
		{"empty view", LogView{ProjectID: "demo-project", Location: "global", Bucket: "_Default", View: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.view.resourceName("demo-project"); err == nil {
				t.Fatal("invalid log view accepted")
			}
		})
	}
	cfg := Config{ID: "logs", ProjectID: "demo-project", Collection: "entries"}
	if _, err := NewPersonalDB(context.Background(), cfg, validLogView(), nil); err == nil {
		t.Fatal("missing trusted token source accepted")
	}
}

func TestPersonalDBUsesTrustedCredentialAndFixedView(t *testing.T) {
	const resource = "projects/demo-project/locations/global/buckets/_Default/views/_AllLogs"
	var page loggingv2.ListLogEntriesRequest
	provider := staticTokenProvider{token: &auth.Token{Value: "user-access", Type: "Bearer"}}
	client, err := httptransport.NewClient(&httptransport.Options{Credentials: auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: provider}), BaseRoundTripper: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/entries:list") {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer user-access" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&page); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return apiResponse(request, `{"entries":[{"timestamp":"2026-10-19T03:30:00Z","severity":"ERROR","logName":"projects/demo-project/logs/app","jsonPayload":{"message":"hello","order_id":"11039"},"insertId":"abc"}]}`), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{ID: "logs", ProjectID: "demo-project", Collection: "entries", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Second}
	db, err := newPersonalDB(context.Background(), cfg, validLogView(), provider, option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	defer db.(interface{ Close() error }).Close()
	reader, err := db.ExecuteQueryToRecordsReader(context.Background(), boundedQuery())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	row, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	if row.Data().(map[string]any)["message"] != "hello" {
		t.Fatalf("row = %#v", row.Data())
	}
	if len(page.ResourceNames) != 1 || page.ResourceNames[0] != resource || strings.Contains(strings.Join(page.ResourceNames, ","), "other-project") {
		t.Fatalf("resource_names = %#v", page.ResourceNames)
	}
	if page.PageSize != 10 || page.OrderBy != "timestamp desc" || !strings.Contains(page.Filter, `jsonPayload.order_id="11039"`) {
		t.Fatalf("request filter/order/page size = %#v", page)
	}
}

func TestPersonalPaginationContinuesAfterEmptyPage(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		requests++
		var page loggingv2.ListLogEntriesRequest
		if err := json.NewDecoder(request.Body).Decode(&page); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if requests == 1 {
			if page.PageToken != "" {
				t.Errorf("first page token = %q", page.PageToken)
			}
			return apiResponse(request, `{"entries":[],"nextPageToken":"continue"}`), nil
		}
		if page.PageToken != "continue" {
			t.Errorf("second page token = %q", page.PageToken)
		}
		return apiResponse(request, `{"entries":[{"textPayload":"after empty page"}]}`), nil
	})}
	service, err := loggingv2.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	list := func(request *loggingv2.ListLogEntriesRequest) *loggingv2.EntriesListCall {
		return service.Entries.List(request)
	}
	reader := &personalEntryReader{ctx: context.Background(), list: list, request: &loggingv2.ListLogEntriesRequest{ResourceNames: []string{"projects/demo-project/locations/global/buckets/_Default/views/_AllLogs"}, PageSize: 10}}
	entry, err := reader.Next()
	if err != nil || entry.Payload != "after empty page" {
		t.Fatalf("entry = %#v, %v", entry, err)
	}
	if _, err := reader.Next(); err != iterator.Done {
		t.Fatalf("end = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
}

func TestPersonalPaginationIsBoundedAndCancellationPropagates(t *testing.T) {
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		requests++
		return apiResponse(request, `{"entries":[],"nextPageToken":"more"}`), nil
	})}
	service, err := loggingv2.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	list := func(request *loggingv2.ListLogEntriesRequest) *loggingv2.EntriesListCall {
		return service.Entries.List(request)
	}
	reader := &personalEntryReader{ctx: context.Background(), list: list, request: &loggingv2.ListLogEntriesRequest{ResourceNames: []string{"projects/demo-project/locations/global/buckets/_Default/views/_AllLogs"}, PageSize: 1}}
	if _, err := reader.Next(); err == nil || !strings.Contains(err.Error(), "page budget") {
		t.Fatalf("page budget result = %v", err)
	}
	if requests != maxPersonalPages {
		t.Fatalf("page calls = %d, want %d", requests, maxPersonalPages)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader = &personalEntryReader{ctx: ctx, list: list, request: &loggingv2.ListLogEntriesRequest{ResourceNames: []string{"projects/demo-project/locations/global/buckets/_Default/views/_AllLogs"}, PageSize: 1}}
	if _, err := reader.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestPersonalRawEvidenceIncludesRESTOnlyFields(t *testing.T) {
	const response = `{"entries":[{"textPayload":"same mapped payload","split":{"uid":"split-1","index":0,"totalSplits":2},"receiveTimestamp":"2026-10-19T03:31:00Z","metadata":{"userLabels":{"evidence":"metadata"}},"errorGroups":[{"id":"group-1"}],"apphub":{"application":{"id":"app-1"}},"apphubSource":{"application":{"id":"source-1"}},"apphubDestination":{"application":{"id":"destination-1"}},"otel":{"resource":{"attributes":{"service.name":"orders"}}}}]}`
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return apiResponse(request, response), nil
	})}
	service, err := loggingv2.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	reader := &personalEntryReader{
		ctx: context.Background(),
		list: func(request *loggingv2.ListLogEntriesRequest) *loggingv2.EntriesListCall {
			return service.Entries.List(request)
		},
		request: &loggingv2.ListLogEntriesRequest{ResourceNames: []string{"projects/demo-project/locations/global/buckets/_Default/views/_AllLogs"}, PageSize: 1},
	}
	entry, source, err := reader.NextWithSource()
	if err != nil {
		t.Fatal(err)
	}
	row, err := mapEntryWithSource(entry, "demo-project", source)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(row["raw"].(string)), &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"split", "receiveTimestamp", "metadata", "errorGroups", "apphub", "apphubSource", "apphubDestination", "otel"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("raw evidence omitted REST field %q: %s", field, row["raw"])
		}
	}
	if got := raw["receiveTimestamp"]; got != "2026-10-19T03:31:00Z" {
		t.Errorf("receiveTimestamp = %#v", got)
	}

	// These REST-only fields do not map to logging.Entry. They must still
	// contribute to source identity, even when the user-facing mapped row is
	// otherwise identical.
	alteredSourceFields := map[string]any{}
	if err := json.Unmarshal(source, &alteredSourceFields); err != nil {
		t.Fatal(err)
	}
	alteredSplit := alteredSourceFields["split"].(map[string]any)
	alteredSplit["uid"] = "split-2"
	alteredSource, err := json.Marshal(alteredSourceFields)
	if err != nil {
		t.Fatal(err)
	}
	row2, err := mapEntryWithSource(entry, "demo-project", alteredSource)
	if err != nil {
		t.Fatal(err)
	}
	if row["source_ref"] == row2["source_ref"] {
		t.Fatal("REST-only evidence change did not change source_ref")
	}
}

func TestPersonalQueryCancellationStopsTokenRefresh(t *testing.T) {
	started := make(chan struct{})
	provider := tokenProviderFunc(func(ctx context.Context) (*auth.Token, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	cfg := Config{ID: "logs", ProjectID: "demo-project", Collection: "entries", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Second}
	db, err := newPersonalDB(context.Background(), cfg, validLogView(), provider)
	if err != nil {
		t.Fatal(err)
	}
	defer db.(interface{ Close() error }).Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := db.ExecuteQueryToRecordsReader(ctx, boundedQuery())
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("token provider was not called")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("query after refresh cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("query did not stop after refresh cancellation")
	}
}

type tokenProviderFunc func(context.Context) (*auth.Token, error)

func (f tokenProviderFunc) Token(ctx context.Context) (*auth.Token, error) { return f(ctx) }

func TestPersonalConfigLimitsRemainBounded(t *testing.T) {
	cfg := Config{ID: "logs", ProjectID: "demo-project", Collection: "entries", MaxResults: 1001}
	if _, err := newPersonalDB(context.Background(), cfg, validLogView(), staticTokenProvider{token: &auth.Token{Value: "token", Type: "Bearer"}}); err == nil {
		t.Fatal("too many results accepted")
	}
	if _, _, err := buildFilter(boundedQuery(), Config{Collection: "other", MaxResults: 20, MaxWindow: time.Hour}); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("collection escape = %v", err)
	}
}

func TestPersonalEntryConversionPreservesEvidenceAndReferenceIdentity(t *testing.T) {
	baseline, err := convertPersonalEntry(&loggingv2.LogEntry{TextPayload: "same"})
	if err != nil {
		t.Fatal(err)
	}
	baselineRow, err := mapEntry(baseline, "demo-project")
	if err != nil {
		t.Fatal(err)
	}
	httpEntry, err := convertPersonalEntry(&loggingv2.LogEntry{TextPayload: "same", HttpRequest: &loggingv2.HttpRequest{
		RequestMethod: "POST", RequestUrl: "https://example.test/orders?id=1", Protocol: "HTTP/2",
		UserAgent: "test-agent", RequestSize: 17, Status: 503, ResponseSize: 31, Latency: "250ms",
		RemoteIp: "192.0.2.10", ServerIp: "192.0.2.20", CacheHit: true, CacheLookup: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if httpEntry.HTTPRequest == nil || httpEntry.HTTPRequest.Request == nil || httpEntry.HTTPRequest.Request.URL.String() != "https://example.test/orders?id=1" || httpEntry.HTTPRequest.Status != 503 || httpEntry.HTTPRequest.Latency != 250*time.Millisecond {
		t.Fatalf("HTTP request evidence was not preserved: %#v", httpEntry.HTTPRequest)
	}
	operationEntry, err := convertPersonalEntry(&loggingv2.LogEntry{TextPayload: "same", Operation: &loggingv2.LogEntryOperation{Id: "op-1", Producer: "worker", First: true, Last: true}})
	if err != nil || operationEntry.Operation == nil || operationEntry.Operation.Id != "op-1" || !operationEntry.Operation.First || !operationEntry.Operation.Last {
		t.Fatalf("operation evidence = %#v, %v", operationEntry, err)
	}
	sourceEntry, err := convertPersonalEntry(&loggingv2.LogEntry{TextPayload: "same", SourceLocation: &loggingv2.LogEntrySourceLocation{File: "worker.go", Line: 42, Function: "processOrder"}})
	if err != nil || sourceEntry.SourceLocation == nil || sourceEntry.SourceLocation.File != "worker.go" || sourceEntry.SourceLocation.Line != 42 {
		t.Fatalf("source location evidence = %#v, %v", sourceEntry, err)
	}
	for name, entry := range map[string]*logging.Entry{"HTTPRequest": httpEntry, "Operation": operationEntry, "SourceLocation": sourceEntry} {
		row, err := mapEntry(entry, "demo-project")
		if err != nil {
			t.Fatal(err)
		}
		if row["source_ref"] == baselineRow["source_ref"] || !strings.Contains(row["raw"].(string), name) {
			t.Errorf("%s missing from raw evidence or source reference did not change: %#v", name, row)
		}
	}
}

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

	"github.com/dal-go/dalgo/dal"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	loggingv2 "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
)

func validLogView() LogView {
	return LogView{ProjectID: "demo-project", Location: "global", Bucket: "_Default", View: "_AllLogs"}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func apiResponse(request *http.Request, body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func authHTTPClient(source oauth2.TokenSource, roundTrip roundTripFunc) *http.Client {
	return &http.Client{Transport: &oauth2.Transport{Source: source, Base: roundTrip}}
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
	source := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "user-access"})
	client := authHTTPClient(source, func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/entries:list") {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer user-access" {
			t.Errorf("Authorization = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&page); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return apiResponse(request, `{"entries":[{"timestamp":"2026-10-19T03:30:00Z","severity":"ERROR","logName":"projects/demo-project/logs/app","textPayload":"hello","insertId":"abc"}]}`), nil
	})
	cfg := Config{ID: "logs", ProjectID: "demo-project", Collection: "entries", MaxResults: 20, MaxWindow: time.Hour, MaxQueryDuration: time.Second}
	db, err := newPersonalDB(context.Background(), cfg, validLogView(), source, option.WithHTTPClient(client))
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

func TestPersonalConfigLimitsRemainBounded(t *testing.T) {
	cfg := Config{ID: "logs", ProjectID: "demo-project", Collection: "entries", MaxResults: 1001}
	if _, err := newPersonalDB(context.Background(), cfg, validLogView(), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "token"})); err == nil {
		t.Fatal("too many results accepted")
	}
	if _, _, err := buildFilter(boundedQuery(), Config{Collection: "other", MaxResults: 20, MaxWindow: time.Hour}); !errors.Is(err, dal.ErrNotSupported) {
		t.Fatalf("collection escape = %v", err)
	}
}

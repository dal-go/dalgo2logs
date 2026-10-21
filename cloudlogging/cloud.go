// Package cloudlogging exposes one explicitly configured Google Cloud Logging
// project as a read-only DALGO collection. It does not ingest or store logs.
package cloudlogging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/logging"
	logpb "cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/logging/logadmin"
	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
	"google.golang.org/api/iterator"
	mrpb "google.golang.org/genproto/googleapis/api/monitoredres"
)

type Config struct {
	ID               string
	ProjectID        string // fixed by trusted server configuration, never a query parameter
	Collection       string
	MaxResults       int
	MaxWindow        time.Duration
	MaxQueryDuration time.Duration
	MaxEntryBytes    int // complete mapped entry plus response envelope allowance
	MaxScanBytes     int // all entries examined, including local predicate misses
	MaxResultBytes   int // entries returned after local predicate checks
	MaxPages         int // personal Cloud Logging API pages
}

const maxEntryBytes = 1 << 20
const maxResultBytes = 16 << 20
const maxPersonalPages = 10

// LimitKind identifies which configured budget stopped a query.
type LimitKind string

const (
	LimitResults     LimitKind = "results"
	LimitEntryBytes  LimitKind = "entry_bytes"
	LimitScanBytes   LimitKind = "scan_bytes"
	LimitResultBytes LimitKind = "result_bytes"
	LimitPages       LimitKind = "pages"
)

// ErrLimitExceeded lets callers distinguish an incomplete query from no matches.
var ErrLimitExceeded = errors.New("cloudlogging: query limit exceeded")

// LimitError reports a configured limit without exposing log content.
// It retains dal.ErrNotSupported compatibility for existing DALGO callers.
type LimitError struct {
	Kind  LimitKind
	Limit int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("cloudlogging: %s limit exceeded (%d)", e.Kind, e.Limit)
}

func (*LimitError) Unwrap() error        { return dal.ErrNotSupported }
func (*LimitError) Is(target error) bool { return target == ErrLimitExceeded }

type database struct {
	dal.NoConcurrency
	cfg     Config
	client  *logadmin.Client
	closeFn func() error
	entries func(context.Context, string, int) entryReader
}

type entryReader interface {
	Next() (*logging.Entry, error)
}

// sourceEntryReader carries provider fields that the Cloud Logging SDK does not
// model. They remain part of the canonical raw evidence and source identity.
type sourceEntryReader interface {
	NextWithSource() (*logging.Entry, []byte, error)
}

type cloudHandle struct {
	dal.DB
	dbschema.SchemaReader
	closeFn func() error
}

func (h *cloudHandle) Close() error { return h.closeFn() }

var _ dal.Backend = (*database)(nil)
var _ dbschema.SchemaReader = (*database)(nil)
var projectID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// NewDB uses Application Default Credentials. The caller must grant only
// logging.logEntries.list on the configured project and enforce user access to
// this server-side connection before a request reaches the OVDB handler.
func NewDB(ctx context.Context, cfg Config) (dal.DB, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := logadmin.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("cloudlogging: create read client: %w", err)
	}
	backend := &database{cfg: cfg, client: client, entries: func(ctx context.Context, filter string, limit int) entryReader {
		return client.Entries(ctx, logadmin.Filter(filter), logadmin.NewestFirst(), logadmin.PageSize(int32(limit)))
	}}
	backend.closeFn = client.Close
	return &cloudHandle{DB: dal.NewDB(backend), SchemaReader: backend, closeFn: client.Close}, nil
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.ID == "" || cfg.Collection == "" || !projectID.MatchString(cfg.ProjectID) {
		return Config{}, errors.New("cloudlogging: ID, Collection and a valid ProjectID are required")
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = 200
	}
	if cfg.MaxWindow == 0 {
		cfg.MaxWindow = 24 * time.Hour
	}
	if cfg.MaxQueryDuration == 0 {
		cfg.MaxQueryDuration = 30 * time.Second
	}
	if cfg.MaxEntryBytes == 0 {
		cfg.MaxEntryBytes = maxEntryBytes
	}
	if cfg.MaxScanBytes == 0 {
		cfg.MaxScanBytes = maxResultBytes
	}
	if cfg.MaxResultBytes == 0 {
		cfg.MaxResultBytes = maxResultBytes
	}
	if cfg.MaxPages == 0 {
		cfg.MaxPages = maxPersonalPages
	}
	if cfg.MaxResults < 1 || cfg.MaxResults > 1000 || cfg.MaxWindow < time.Minute || cfg.MaxWindow > 7*24*time.Hour || cfg.MaxQueryDuration < time.Second || cfg.MaxQueryDuration > 5*time.Minute {
		return Config{}, errors.New("cloudlogging: limits exceed supported bounds")
	}
	if cfg.MaxEntryBytes < 1 || cfg.MaxEntryBytes > maxEntryBytes || cfg.MaxScanBytes < 1 || cfg.MaxScanBytes > maxResultBytes || cfg.MaxResultBytes < 1 || cfg.MaxResultBytes > maxResultBytes || cfg.MaxPages < 1 || cfg.MaxPages > maxPersonalPages {
		return Config{}, errors.New("cloudlogging: byte or page budgets exceed supported bounds")
	}
	return cfg, nil
}

func (d *database) Close() error {
	if d.closeFn != nil {
		return d.closeFn()
	}
	if d.client != nil {
		return d.client.Close()
	}
	return nil
}
func (d *database) ID() string         { return d.cfg.ID }
func (*database) Adapter() dal.Adapter { return dal.NewAdapter("dalgo2logs/cloudlogging", "0.1") }
func (*database) Schema() dal.Schema   { return nil }
func (d *database) RunReadonlyTransaction(ctx context.Context, f dal.ROTxWorker, opts ...dal.TransactionOption) error {
	return f(ctx, readTx{database: d, opts: dal.NewTransactionOptions(opts...)})
}
func (*database) RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error {
	return fmt.Errorf("%w: Cloud Logging source is read-only", dal.ErrNotSupported)
}

type readTx struct {
	*database
	opts dal.TransactionOptions
}

func (t readTx) Options() dal.TransactionOptions           { return t.opts }
func (*database) Get(context.Context, record.Record) error { return dal.ErrNotSupported }
func (*database) Exists(context.Context, *record.Key) (bool, error) {
	return false, dal.ErrNotSupported
}
func (*database) GetMulti(context.Context, []record.Record) error { return dal.ErrNotSupported }
func (*database) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}

func buildFilter(q dal.StructuredQuery, cfg Config) (string, int, error) {
	if q.From() == nil || q.From().Base() == nil || q.From().Base().Name() != cfg.Collection || len(q.From().Joins()) > 0 || len(q.GroupBy()) > 0 || q.Having() != nil || q.Offset() != 0 || q.StartFrom() != "" || q.StartAfter() != "" {
		return "", 0, fmt.Errorf("%w: unsupported Cloud Logging query shape", dal.ErrNotSupported)
	}
	if len(q.OrderBy()) > 1 {
		return "", 0, fmt.Errorf("%w: only newest-first timestamp ordering is supported", dal.ErrNotSupported)
	}
	if len(q.OrderBy()) == 1 {
		order := q.OrderBy()[0]
		field, ok := order.Expression().(dal.FieldRef)
		if !ok || field.Name() != "timestamp" || !order.Descending() {
			return "", 0, fmt.Errorf("%w: only newest-first timestamp ordering is supported", dal.ErrNotSupported)
		}
	}
	if len(q.Columns()) > 0 {
		return "", 0, fmt.Errorf("%w: projection is unavailable", dal.ErrNotSupported)
	}
	limit := q.Limit()
	if limit == 0 {
		limit = cfg.MaxResults
	}
	if limit < 1 || limit > cfg.MaxResults {
		return "", 0, &LimitError{Kind: LimitResults, Limit: cfg.MaxResults}
	}
	var parts []string
	var lower, upper time.Time
	comparisons := 0
	var walk func(dal.Condition) error
	walk = func(cond dal.Condition) error {
		switch c := cond.(type) {
		case dal.GroupCondition:
			if c.Operator() != dal.And {
				return fmt.Errorf("%w: only AND filters are supported", dal.ErrNotSupported)
			}
			for _, child := range c.Conditions() {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		case dal.Comparison:
			comparisons++
			if comparisons > 16 {
				return fmt.Errorf("%w: too many Cloud Logging filters", dal.ErrNotSupported)
			}
			field, ok := c.Left.(dal.FieldRef)
			if !ok || field.Source() != "" {
				return fmt.Errorf("%w: unsupported filter field", dal.ErrNotSupported)
			}
			v, ok := c.Right.(dal.Constant)
			if !ok {
				return fmt.Errorf("%w: constant filter required", dal.ErrNotSupported)
			}
			value, ok := v.Value.(string)
			if !ok {
				return fmt.Errorf("%w: string filter required", dal.ErrNotSupported)
			}
			switch field.Name() {
			case "timestamp":
				t, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					return fmt.Errorf("%w: RFC3339 timestamp required", dal.ErrNotSupported)
				}
				t = t.UTC()
				switch c.Operator {
				case dal.GreaterOrEqual:
					lower = t
				case dal.LessOrEqual:
					upper = t
				default:
					return fmt.Errorf("%w: use inclusive timestamp bounds", dal.ErrNotSupported)
				}
				parts = append(parts, fmt.Sprintf("timestamp %s %s", c.Operator, strconv.Quote(t.Format(time.RFC3339Nano))))
			case "severity":
				if c.Operator != dal.Equal {
					return dal.ErrNotSupported
				}
				value = strings.ToUpper(value)
				switch value {
				case "DEFAULT", "DEBUG", "INFO", "NOTICE", "WARNING", "ERROR", "CRITICAL", "ALERT", "EMERGENCY":
				default:
					return dal.ErrNotSupported
				}
				parts = append(parts, "severity="+value)
			case "correlation_id", "order_id", "service":
				if c.Operator != dal.Equal || len(value) > 256 || !isASCII(value) {
					return dal.ErrNotSupported
				}
				quoted := strconv.Quote(value)
				clause := fmt.Sprintf("(jsonPayload.%s=%s OR labels.%s=%s)", field.Name(), quoted, field.Name(), quoted)
				if field.Name() == "correlation_id" {
					clause = fmt.Sprintf("(jsonPayload.correlation_id=%s OR labels.correlation_id=%s OR trace=%s)", quoted, quoted, quoted)
				} else if field.Name() == "service" {
					clause = fmt.Sprintf("(jsonPayload.service=%s OR labels.service=%s OR resource.labels.service_name=%s)", quoted, quoted, quoted)
				}
				parts = append(parts, clause)
			default:
				return fmt.Errorf("%w: filter is unavailable", dal.ErrNotSupported)
			}
			return nil
		default:
			return fmt.Errorf("%w: condition shape is unavailable", dal.ErrNotSupported)
		}
	}
	if err := walk(q.Where()); err != nil {
		return "", 0, err
	}
	if lower.IsZero() || upper.IsZero() || upper.Before(lower) || upper.Sub(lower) > cfg.MaxWindow {
		return "", 0, fmt.Errorf("%w: bounded inclusive time range required", dal.ErrNotSupported)
	}
	filter := strings.Join(parts, " AND ")
	if len(filter) > 8192 {
		return "", 0, fmt.Errorf("%w: Cloud Logging filter is too large", dal.ErrNotSupported)
	}
	return filter, limit, nil
}

func (d *database) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	q, ok := query.(dal.StructuredQuery)
	if !ok {
		return nil, dal.ErrNotSupported
	}
	filter, limit, err := buildFilter(q, d.cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.cfg.MaxQueryDuration)
	defer cancel()
	it := d.entries(ctx, filter, limit)
	records := make([]record.Record, 0, limit)
	scanBytes, resultBytes := 0, 0
	for len(records) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var e *logging.Entry
		var source []byte
		var err error
		if sourced, ok := it.(sourceEntryReader); ok {
			e, source, err = sourced.NextWithSource()
		} else {
			e, err = it.Next()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cloudlogging: list entries failed: %w", err)
		}
		row, err := mapEntryWithSource(e, d.cfg.ProjectID, source)
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, errors.New("cloudlogging: mapped entry cannot be encoded")
		}
		// Reserve envelope/key overhead for the normal OVDB response in addition
		// to the complete mapped row, whose payload appears in several fields.
		entryBytes := len(encoded) + 1024
		if entryBytes > d.cfg.entryBudget() {
			return nil, &LimitError{Kind: LimitEntryBytes, Limit: d.cfg.entryBudget()}
		}
		if scanBytes+entryBytes > d.cfg.scanBudget() {
			return nil, &LimitError{Kind: LimitScanBytes, Limit: d.cfg.scanBudget()}
		}
		scanBytes += entryBytes
		matches, err := matchesQuery(q.Where(), row)
		if err != nil {
			return nil, err
		}
		if !matches {
			continue
		}
		if resultBytes+entryBytes > d.cfg.resultBudget() {
			return nil, &LimitError{Kind: LimitResultBytes, Limit: d.cfg.resultBudget()}
		}
		resultBytes += entryBytes
		id := row["source_ref"].(string)
		if template := q.IntoRecord(); template != nil {
			if err := record.MapToData(template.Data(), row); err != nil {
				return nil, err
			}
			records = append(records, record.NewRecordWithData(record.NewKeyWithID(d.cfg.Collection, id), template.Data()))
		} else {
			records = append(records, record.NewRecordWithData(record.NewKeyWithID(d.cfg.Collection, id), row))
		}
	}
	return dal.NewRecordsReader(records), nil
}

// Zero-value budget fallbacks also support package-local test backends that
// construct database directly instead of calling NewDB/NewPersonalDB.
func (c Config) entryBudget() int {
	if c.MaxEntryBytes != 0 {
		return c.MaxEntryBytes
	}
	return maxEntryBytes
}
func (c Config) scanBudget() int {
	if c.MaxScanBytes != 0 {
		return c.MaxScanBytes
	}
	return maxResultBytes
}
func (c Config) resultBudget() int {
	if c.MaxResultBytes != 0 {
		return c.MaxResultBytes
	}
	return maxResultBytes
}

func matchesQuery(condition dal.Condition, row map[string]any) (bool, error) {
	switch c := condition.(type) {
	case dal.GroupCondition:
		if c.Operator() != dal.And {
			return false, fmt.Errorf("%w: only AND filters are supported", dal.ErrNotSupported)
		}
		for _, child := range c.Conditions() {
			matches, err := matchesQuery(child, row)
			if err != nil || !matches {
				return matches, err
			}
		}
		return true, nil
	case dal.Comparison:
		field, ok := c.Left.(dal.FieldRef)
		if !ok {
			return false, fmt.Errorf("%w: unsupported filter field", dal.ErrNotSupported)
		}
		constant, ok := c.Right.(dal.Constant)
		if !ok {
			return false, fmt.Errorf("%w: constant filter required", dal.ErrNotSupported)
		}
		expected, ok := constant.Value.(string)
		if !ok {
			return false, fmt.Errorf("%w: string filter required", dal.ErrNotSupported)
		}
		actual, ok := row[field.Name()].(string)
		if !ok {
			return false, nil
		}
		switch field.Name() {
		case "timestamp":
			expectedTime, err := time.Parse(time.RFC3339Nano, expected)
			if err != nil {
				return false, fmt.Errorf("%w: RFC3339 timestamp required", dal.ErrNotSupported)
			}
			actualTime, err := time.Parse(time.RFC3339Nano, actual)
			if err != nil {
				return false, nil
			}
			switch c.Operator {
			case dal.GreaterOrEqual:
				return !actualTime.Before(expectedTime), nil
			case dal.LessOrEqual:
				return !actualTime.After(expectedTime), nil
			default:
				return false, fmt.Errorf("%w: use inclusive timestamp bounds", dal.ErrNotSupported)
			}
		case "severity", "correlation_id", "order_id", "service":
			if c.Operator != dal.Equal {
				return false, fmt.Errorf("%w: equality filter required", dal.ErrNotSupported)
			}
			if !isASCII(actual) || !isASCII(expected) {
				return false, fmt.Errorf("%w: local equality checks require ASCII values", dal.ErrNotSupported)
			}
			return strings.EqualFold(actual, expected), nil
		default:
			return false, fmt.Errorf("%w: filter is unavailable", dal.ErrNotSupported)
		}
	default:
		return false, fmt.Errorf("%w: condition shape is unavailable", dal.ErrNotSupported)
	}
}

func mapEntry(e *logging.Entry, projectID string) (map[string]any, error) {
	return mapEntryWithSource(e, projectID, nil)
}

func mapEntryWithSource(e *logging.Entry, projectID string, source []byte) (map[string]any, error) {
	row := map[string]any{"parse_status": "parsed", "severity": strings.ToUpper(e.Severity.String()), "source": e.LogName, "parser": "google-cloud-logging", "project_id": projectID, "log_name": e.LogName, "insert_id": e.InsertID}
	if !e.Timestamp.IsZero() {
		row["timestamp"] = e.Timestamp.UTC().Format(time.RFC3339Nano)
	} else {
		row["parse_status"] = "missing_timestamp"
	}
	if e.Trace != "" {
		row["correlation_id"] = e.Trace
		row["trace_id"] = e.Trace
	}
	for _, name := range []string{"correlation_id", "order_id", "service"} {
		if value := e.Labels[name]; value != "" {
			row[name] = value
		}
	}
	if e.Resource != nil {
		if service := e.Resource.Labels["service_name"]; service != "" {
			row["service"] = service
		}
	}
	if obj, ok := e.Payload.(map[string]any); ok {
		for _, name := range []string{"message", "correlation_id", "order_id", "service"} {
			if value, ok := obj[name]; ok && value != nil {
				row[name] = fmt.Sprint(value)
			}
		}
	} else if text, ok := e.Payload.(string); ok {
		row["message"] = text
	}
	if _, ok := row["message"]; !ok {
		row["message"] = "Structured Cloud Logging entry"
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return nil, errors.New("cloudlogging: entry payload cannot be encoded")
	}
	row["fields_json"] = string(payload)
	labels, err := json.Marshal(e.Labels)
	if err != nil {
		return nil, errors.New("cloudlogging: entry labels cannot be encoded")
	}
	row["labels_json"] = string(labels)
	if len(source) == 0 {
		var err error
		source, err = marshalEntry(e)
		if err != nil {
			return nil, errors.New("cloudlogging: entry cannot be encoded")
		}
	}
	row["raw"] = string(source) // canonical provider entry, including REST-only evidence fields
	digest := sha256.Sum256(append([]byte(projectID+"\x00"), source...))
	row["source_ref"] = "gcl:" + hex.EncodeToString(digest[:])
	return row, nil
}

func isASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] >= 0x80 {
			return false
		}
	}
	return true
}

// marshalEntry preserves the SDK's successful JSON bytes without asking JSON to
// encode http.Request's unsupported function fields. A populated request keeps
// the existing canonical HTTP evidence projection, not the SDK write conversion.
func marshalEntry(e *logging.Entry) ([]byte, error) {
	if e.HTTPRequest == nil || e.HTTPRequest.Request == nil {
		var request *sdkHTTPRequestJSON
		if h := e.HTTPRequest; h != nil {
			request = &sdkHTTPRequestJSON{
				RequestSize: h.RequestSize, Status: h.Status, ResponseSize: h.ResponseSize,
				Latency: h.Latency, LocalIP: h.LocalIP, RemoteIP: h.RemoteIP,
				CacheHit: h.CacheHit, CacheValidatedWithOriginServer: h.CacheValidatedWithOriginServer,
				CacheFillBytes: h.CacheFillBytes, CacheLookup: h.CacheLookup,
			}
		}
		// Field order and nulls match logging.Entry at the pinned SDK version.
		return json.Marshal(struct {
			Timestamp      time.Time
			Severity       logging.Severity
			Payload        any
			Labels         map[string]string
			InsertID       string
			HTTPRequest    *sdkHTTPRequestJSON
			Operation      *logpb.LogEntryOperation
			LogName        string
			Resource       *mrpb.MonitoredResource
			Trace          string
			SpanID         string
			TraceSampled   bool
			SourceLocation *logpb.LogEntrySourceLocation
		}{e.Timestamp, e.Severity, e.Payload, e.Labels, e.InsertID, request, e.Operation, e.LogName, e.Resource, e.Trace, e.SpanID, e.TraceSampled, e.SourceLocation})
	}
	if e.HTTPRequest.Request.URL == nil {
		return nil, errors.New("cloudlogging: HTTP request URL is missing")
	}
	var request *canonicalHTTPRequest
	if h := e.HTTPRequest; h != nil {
		request = &canonicalHTTPRequest{
			RequestSize: h.RequestSize, Status: h.Status, ResponseSize: h.ResponseSize,
			Latency: h.Latency.String(), LocalIP: h.LocalIP, RemoteIP: h.RemoteIP,
			CacheHit: h.CacheHit, CacheValidatedWithOriginServer: h.CacheValidatedWithOriginServer,
			CacheFillBytes: h.CacheFillBytes, CacheLookup: h.CacheLookup,
		}
		if h.Request != nil {
			request.Method = h.Request.Method
			request.URL = h.Request.URL.String()
			request.Proto = h.Request.Proto
			request.Header = h.Request.Header
		}
	}
	return json.Marshal(struct {
		Timestamp      time.Time                     `json:"Timestamp"`
		Severity       logging.Severity              `json:"Severity"`
		Payload        any                           `json:"Payload"`
		Labels         map[string]string             `json:"Labels"`
		InsertID       string                        `json:"InsertID"`
		HTTPRequest    *canonicalHTTPRequest         `json:"HTTPRequest,omitempty"`
		Operation      *logpb.LogEntryOperation      `json:"Operation,omitempty"`
		LogName        string                        `json:"LogName"`
		Resource       *mrpb.MonitoredResource       `json:"Resource,omitempty"`
		Trace          string                        `json:"Trace"`
		SpanID         string                        `json:"SpanID"`
		TraceSampled   bool                          `json:"TraceSampled"`
		SourceLocation *logpb.LogEntrySourceLocation `json:"SourceLocation,omitempty"`
	}{e.Timestamp, e.Severity, e.Payload, e.Labels, e.InsertID, request, e.Operation, e.LogName, e.Resource, e.Trace, e.SpanID, e.TraceSampled, e.SourceLocation})
}

// sdkHTTPRequestJSON is the SDK HTTPRequest encoding when Request is nil. The
// placeholder retains its JSON null without exposing http.Request functions.
type sdkHTTPRequestJSON struct {
	Request                        *struct{}
	RequestSize                    int64
	Status                         int
	ResponseSize                   int64
	Latency                        time.Duration
	LocalIP                        string
	RemoteIP                       string
	CacheHit                       bool
	CacheValidatedWithOriginServer bool
	CacheFillBytes                 int64
	CacheLookup                    bool
}

type canonicalHTTPRequest struct {
	Method                         string      `json:"Method,omitempty"`
	URL                            string      `json:"URL,omitempty"`
	Proto                          string      `json:"Proto,omitempty"`
	Header                         http.Header `json:"Header,omitempty"`
	RequestSize                    int64       `json:"RequestSize"`
	Status                         int         `json:"Status"`
	ResponseSize                   int64       `json:"ResponseSize"`
	Latency                        string      `json:"Latency"`
	LocalIP                        string      `json:"LocalIP,omitempty"`
	RemoteIP                       string      `json:"RemoteIP,omitempty"`
	CacheHit                       bool        `json:"CacheHit"`
	CacheValidatedWithOriginServer bool        `json:"CacheValidatedWithOriginServer"`
	CacheFillBytes                 int64       `json:"CacheFillBytes"`
	CacheLookup                    bool        `json:"CacheLookup"`
}

func (d *database) ListCollections(context.Context, *record.Key) ([]dal.CollectionRef, error) {
	return []dal.CollectionRef{dal.NewRootCollectionRef(d.cfg.Collection, "")}, nil
}
func (d *database) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if ref == nil || ref.Name() != d.cfg.Collection {
		return nil, dal.ErrNotSupported
	}
	fields := []dbschema.FieldDef{{Name: "timestamp", Type: dbschema.String}, {Name: "severity", Type: dbschema.String}, {Name: "message", Type: dbschema.String}, {Name: "service", Type: dbschema.String}, {Name: "correlation_id", Type: dbschema.String}, {Name: "trace_id", Type: dbschema.String}, {Name: "order_id", Type: dbschema.String}, {Name: "source", Type: dbschema.String}, {Name: "source_ref", Type: dbschema.String}, {Name: "raw", Type: dbschema.String}, {Name: "fields_json", Type: dbschema.String}, {Name: "labels_json", Type: dbschema.String}, {Name: "project_id", Type: dbschema.String}, {Name: "log_name", Type: dbschema.String}, {Name: "insert_id", Type: dbschema.String}, {Name: "parse_status", Type: dbschema.String}, {Name: "parser", Type: dbschema.String}}
	return &dbschema.CollectionDef{Name: d.cfg.Collection, Fields: fields}, nil
}
func (*database) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}
func (*database) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, dal.ErrNotSupported
}
func (*database) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, dal.ErrNotSupported
}

package dalgo2logs

import (
	"context"
	"fmt"
	"sort"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/record"
)

var _ dbschema.SchemaReader = (*database)(nil)

func (d *database) ListCollections(_ context.Context, parent *record.Key) ([]dal.CollectionRef, error) {
	if parent != nil {
		return nil, &dbschema.NotSupportedError{Op: "ListCollections", Backend: "dalgo2logs", Reason: "nested collections are unavailable"}
	}
	names := make([]string, 0, len(d.sources))
	for name := range d.sources {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := make([]dal.CollectionRef, 0, len(names))
	for _, name := range names {
		refs = append(refs, dal.NewRootCollectionRef(name, ""))
	}
	return refs, nil
}

func (d *database) DescribeCollection(_ context.Context, ref *dal.CollectionRef) (*dbschema.CollectionDef, error) {
	if ref == nil {
		return nil, fmt.Errorf("%w: missing collection", dal.ErrNotSupported)
	}
	name := ref.Name()
	if _, ok := d.sources[name]; !ok {
		return nil, fmt.Errorf("%w: unknown collection", dal.ErrNotSupported)
	}
	fields := []dbschema.FieldDef{
		{Name: "id", Type: dbschema.String},
		{Name: "timestamp", Type: dbschema.String, Nullable: true},
		{Name: "severity", Type: dbschema.String, Nullable: true},
		{Name: "message", Type: dbschema.String, Nullable: true},
		{Name: "service", Type: dbschema.String, Nullable: true},
		{Name: "correlation_id", Type: dbschema.String, Nullable: true},
		{Name: "order_id", Type: dbschema.String, Nullable: true},
		{Name: "parse_status", Type: dbschema.String},
		{Name: "source", Type: dbschema.String},
		{Name: "source_ref", Type: dbschema.String},
		{Name: "parser", Type: dbschema.String},
		{Name: "raw", Type: dbschema.String},
		{Name: "fields_json", Type: dbschema.String, Nullable: true},
		{Name: "line_start", Type: dbschema.Int},
		{Name: "line_end", Type: dbschema.Int},
		{Name: "byte_offset", Type: dbschema.Int},
	}
	return &dbschema.CollectionDef{Name: name, Fields: fields, PrimaryKey: []dal.FieldName{"id"}}, nil
}
func (*database) ListIndexes(context.Context, *dal.CollectionRef) ([]dbschema.IndexDef, error) {
	return nil, nil
}
func (*database) ListConstraints(context.Context, *dal.CollectionRef) ([]dbschema.ConstraintDef, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListConstraints", Backend: "dalgo2logs"}
}
func (*database) ListReferrers(context.Context, *dal.CollectionRef) ([]dbschema.Referrer, error) {
	return nil, &dbschema.NotSupportedError{Op: "ListReferrers", Backend: "dalgo2logs"}
}

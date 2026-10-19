package dalgo2logs

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/record"
)

type predicate struct {
	field string
	op    dal.Operator
	value string
}

func (d *database) ExecuteQueryToRecordsReader(ctx context.Context, query dal.Query) (dal.RecordsReader, error) {
	q, ok := query.(dal.StructuredQuery)
	if !ok {
		return nil, fmt.Errorf("%w: structured queries only", dal.ErrNotSupported)
	}
	if q.From() == nil || q.From().Base() == nil || len(q.From().Joins()) != 0 || len(q.GroupBy()) != 0 || q.Having() != nil || q.Offset() != 0 || q.StartFrom() != "" || q.StartAfter() != "" {
		return nil, fmt.Errorf("%w: joins, aggregation, offsets and cursors are unavailable", dal.ErrNotSupported)
	}
	s, ok := d.sources[q.From().Base().Name()]
	if !ok {
		return nil, fmt.Errorf("%w: unknown collection", dal.ErrNotSupported)
	}
	limit := q.Limit()
	if limit == 0 {
		limit = d.cfg.MaxResults
	}
	if limit < 0 || limit > d.cfg.MaxResults {
		return nil, fmt.Errorf("%w: result limit exceeds configured maximum", dal.ErrNotSupported)
	}
	if len(q.OrderBy()) > 1 {
		return nil, fmt.Errorf("%w: only timestamp ordering is supported", dal.ErrNotSupported)
	}
	desc := true
	if len(q.OrderBy()) == 1 {
		order := q.OrderBy()[0]
		field, ok := order.Expression().(dal.FieldRef)
		if !ok || field.Name() != "timestamp" {
			return nil, fmt.Errorf("%w: only timestamp ordering is supported", dal.ErrNotSupported)
		}
		desc = order.Descending()
	}
	for _, col := range q.Columns() {
		field, ok := col.Expression.(dal.FieldRef)
		if !ok || field.Source() != "" {
			return nil, fmt.Errorf("%w: computed projections are unavailable", dal.ErrNotSupported)
		}
		if col.Alias != "" && col.Alias != field.Name() {
			return nil, fmt.Errorf("%w: aliased projections are unavailable", dal.ErrNotSupported)
		}
	}
	filters, err := planFilters(q.Where())
	if err != nil {
		return nil, err
	}
	postReferenceFilter := false
	for _, f := range filters {
		if f.field == "source_ref" {
			postReferenceFilter = true
			break
		}
	}
	rows, err := d.scan(ctx, s, func(data map[string]any) bool { return matchFilters(data, filters) }, postReferenceFilter)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool {
		a, _ := rows[i].Data["timestamp"].(string)
		b, _ := rows[j].Data["timestamp"].(string)
		if a == "" {
			return false
		}
		if b == "" {
			return true
		}
		at, aerr := time.Parse(time.RFC3339Nano, a)
		bt, berr := time.Parse(time.RFC3339Nano, b)
		if aerr != nil || berr != nil || at.Equal(bt) {
			return rows[i].ID < rows[j].ID
		}
		if desc {
			return at.After(bt)
		}
		return at.Before(bt)
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	records := make([]record.Record, 0, len(rows))
	for _, row := range rows {
		data := row.Data
		if len(q.Columns()) > 0 {
			projected := make(map[string]any, len(q.Columns()))
			for _, col := range q.Columns() {
				name := col.Expression.(dal.FieldRef).Name()
				if value, ok := data[name]; ok {
					projected[name] = value
				}
			}
			data = projected
		}
		key := record.NewKeyWithID(s.Collection, row.ID)
		if template := q.IntoRecord(); template != nil {
			if err := record.MapToData(template.Data(), data); err != nil {
				return nil, err
			}
			records = append(records, record.NewRecordWithData(key, template.Data()))
		} else {
			records = append(records, record.NewRecordWithData(key, data))
		}
	}
	return dal.NewRecordsReader(records), nil
}

func planFilters(cond dal.Condition) ([]predicate, error) {
	if cond == nil {
		return nil, nil
	}
	switch c := cond.(type) {
	case dal.GroupCondition:
		if c.Operator() != dal.And {
			return nil, fmt.Errorf("%w: only AND filters are supported", dal.ErrNotSupported)
		}
		var out []predicate
		for _, child := range c.Conditions() {
			p, err := planFilters(child)
			if err != nil {
				return nil, err
			}
			out = append(out, p...)
		}
		return out, nil
	case dal.Comparison:
		field, ok := c.Left.(dal.FieldRef)
		if !ok || field.Source() != "" {
			return nil, fmt.Errorf("%w: filter must name a source field", dal.ErrNotSupported)
		}
		constant, ok := c.Right.(dal.Constant)
		if !ok {
			return nil, fmt.Errorf("%w: filter must use a constant", dal.ErrNotSupported)
		}
		name := field.Name()
		switch name {
		case "severity", "service", "correlation_id", "order_id", "source_ref", "parse_status":
			if c.Operator != dal.Equal {
				return nil, fmt.Errorf("%w: this field supports equality only", dal.ErrNotSupported)
			}
		case "timestamp":
			if c.Operator != dal.Equal && c.Operator != dal.GreaterThen && c.Operator != dal.GreaterOrEqual && c.Operator != dal.LessThen && c.Operator != dal.LessOrEqual {
				return nil, fmt.Errorf("%w: timestamp comparison is unsupported", dal.ErrNotSupported)
			}
		default:
			return nil, fmt.Errorf("%w: field filter is unsupported", dal.ErrNotSupported)
		}
		value, ok := constant.Value.(string)
		if !ok {
			return nil, fmt.Errorf("%w: filter requires a string value", dal.ErrNotSupported)
		}
		if name == "timestamp" {
			t, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nil, fmt.Errorf("%w: timestamp must be RFC3339", dal.ErrNotSupported)
			}
			value = t.UTC().Format(time.RFC3339Nano)
		}
		if name == "severity" {
			value = strings.ToUpper(value)
		}
		return []predicate{{field: name, op: c.Operator, value: value}}, nil
	default:
		return nil, fmt.Errorf("%w: condition shape is unsupported", dal.ErrNotSupported)
	}
}

func matchFilters(data map[string]any, filters []predicate) bool {
	for _, f := range filters {
		value, ok := data[f.field].(string)
		if !ok {
			return false
		}
		if f.field == "timestamp" {
			actual, actualErr := time.Parse(time.RFC3339Nano, value)
			boundary, boundaryErr := time.Parse(time.RFC3339Nano, f.value)
			if actualErr != nil || boundaryErr != nil {
				return false
			}
			switch f.op {
			case dal.Equal:
				if !actual.Equal(boundary) {
					return false
				}
			case dal.GreaterThen:
				if !actual.After(boundary) {
					return false
				}
			case dal.GreaterOrEqual:
				if actual.Before(boundary) {
					return false
				}
			case dal.LessThen:
				if !actual.Before(boundary) {
					return false
				}
			case dal.LessOrEqual:
				if actual.After(boundary) {
					return false
				}
			default:
				return false
			}
			continue
		}
		switch f.op {
		case dal.Equal:
			if value != f.value {
				return false
			}
		case dal.GreaterThen:
			if value <= f.value {
				return false
			}
		case dal.GreaterOrEqual:
			if value < f.value {
				return false
			}
		case dal.LessThen:
			if value >= f.value {
				return false
			}
		case dal.LessOrEqual:
			if value > f.value {
				return false
			}
		default:
			return false
		}
	}
	return true
}

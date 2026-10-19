package dalgo2logs

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/dal-go/dalgo/dal"
	"github.com/dal-go/dalgo/dbschema"
	"github.com/dal-go/dalgo/recordset"
	"github.com/dal-go/record"
)

type database struct {
	dal.NoConcurrency
	cfg     Config
	sources map[string]source
	root    *os.Root
}

type fileHandle struct {
	dal.DB
	dbschema.SchemaReader
	root *os.Root
}

func (h *fileHandle) Close() error { return h.root.Close() }

var _ dal.Backend = (*database)(nil)

func (d *database) ID() string           { return d.cfg.ID }
func (d *database) Adapter() dal.Adapter { return dal.NewAdapter("dalgo2logs", "0.1") }
func (d *database) Schema() dal.Schema   { return nil }

func (d *database) RunReadonlyTransaction(ctx context.Context, f dal.ROTxWorker, opts ...dal.TransactionOption) error {
	return f(ctx, readTx{database: d, options: dal.NewTransactionOptions(opts...)})
}
func (d *database) RunReadwriteTransaction(context.Context, dal.RWTxWorker, ...dal.TransactionOption) error {
	return fmt.Errorf("%w: log sources are read-only", dal.ErrNotSupported)
}

type readTx struct {
	*database
	options dal.TransactionOptions
}

func (t readTx) Options() dal.TransactionOptions { return t.options }

func (d *database) Get(ctx context.Context, rec record.Record) error {
	key := rec.Key()
	s, ok := d.sources[key.Collection()]
	if !ok {
		return dal.NewErrNotFoundByKey(key, nil)
	}
	rows, err := d.scan(ctx, s, nil, false)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == fmt.Sprint(key.ID) {
			if err := record.MapToData(rec.Data(), row.Data); err != nil {
				return err
			}
			rec.SetError(nil)
			return nil
		}
	}
	err = dal.NewErrNotFoundByKey(key, nil)
	rec.SetError(err)
	return err
}
func (d *database) Exists(ctx context.Context, key *record.Key) (bool, error) {
	r := record.NewRecordWithData(key, &map[string]any{})
	err := d.Get(ctx, r)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, record.ErrRecordNotFound) {
		return false, nil
	}
	return false, err
}
func (d *database) GetMulti(ctx context.Context, records []record.Record) error {
	for _, r := range records {
		if err := d.Get(ctx, r); err != nil {
			return err
		}
	}
	return nil
}
func (d *database) ExecuteQueryToRecordsetReader(context.Context, dal.Query, ...recordset.Option) (dal.RecordsetReader, error) {
	return nil, dal.ErrNotSupported
}

package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/joaovillas/devgateway/internal/exchange"
)

// foldEqual is strings.EqualFold exposed to SQL. SQLite's COLLATE NOCASE
// only folds ASCII and would diverge from exchange.Filter.Match on the
// method filter.
const foldEqual = "gateway_equal_fold"

func init() {
	sqlite.MustRegisterDeterministicScalarFunction(foldEqual, 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			a, _ := args[0].(string)
			b, _ := args[1].(string)
			if strings.EqualFold(a, b) {
				return int64(1), nil
			}
			return int64(0), nil
		})
}

// sqliteSchema stores every complete exchange as JSON, next to the columns
// used by the filters and by the ordering. History order is orderKey's:
// (start_sec, start_nsec, seq, ord), where ord, the record order, only
// breaks ties. AUTOINCREMENT keeps a clear from reusing ord values.
//
// The start is kept as separate seconds and nanoseconds, not as nanoseconds
// since the epoch, because UnixNano cannot represent instants outside
// 1678-2262 (the zero instant among them) and the time window has to compare
// the way time.Time.Before does. path is a BLOB so that instr compares
// bytes, the way strings.Contains does.
//
// The gateway_meta table holds the history epoch, which is bumped on every
// clear and invalidates the cursors issued before it.
const sqliteSchema = `
CREATE TABLE IF NOT EXISTS exchanges (
	ord        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT    NOT NULL UNIQUE,
	route      TEXT    NOT NULL,
	upstream   TEXT    NOT NULL,
	override   TEXT    NOT NULL,
	method     TEXT    NOT NULL,
	path       BLOB    NOT NULL,
	status     INTEGER NOT NULL,
	intervened INTEGER NOT NULL,
	start_sec  INTEGER NOT NULL,
	start_nsec INTEGER NOT NULL,
	seq        INTEGER NOT NULL DEFAULT 0,
	data       BLOB    NOT NULL
);
CREATE TABLE IF NOT EXISTS gateway_meta (
	k TEXT PRIMARY KEY,
	v INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS exchanges_route ON exchanges(route);
CREATE INDEX IF NOT EXISTS exchanges_status ON exchanges(status);
CREATE INDEX IF NOT EXISTS exchanges_start ON exchanges(start_sec, start_nsec);
`

// sqliteOrderIndex comes after the migration, which may add seq.
const sqliteOrderIndex = `CREATE INDEX IF NOT EXISTS exchanges_order ON exchanges(start_sec, start_nsec, seq, ord)`

// orderCols is the order key in SQL, in the same order as orderKey.
const orderCols = "(start_sec, start_nsec, seq, ord)"

// SQLite keeps the history in a local SQLite database, through the pure Go
// driver (modernc.org/sqlite), so that the binary still builds without CGO.
// The filters run in SQL and reproduce exchange.Filter.Match.
type SQLite struct {
	db   *sql.DB
	path string
}

// OpenSQLite opens (or creates) the database at path and ensures the schema.
// Opening only completes after a real write, so that a path without write
// permission fails here and not on the first exchange.
func OpenSQLite(path string) (*SQLite, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection: SQLite serializes writes anyway, so concurrent
	// writes queue up in database/sql instead of failing with SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", sqliteSchema} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrateSeq(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating the history at %s: %w", path, err)
	}
	if _, err := db.Exec(sqliteOrderIndex); err != nil {
		db.Close()
		return nil, err
	}
	if err := probeWrite(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("the database does not accept writes: %w", err)
	}
	return &SQLite{db: db, path: path}, nil
}

// migrateSeq adds the seq column to a database created before it, filled in
// from the JSON of each exchange.
func migrateSeq(db *sql.DB) error {
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('exchanges') WHERE name = 'seq'").Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"ALTER TABLE exchanges ADD COLUMN seq INTEGER NOT NULL DEFAULT 0",
		"UPDATE exchanges SET seq = coalesce(json_extract(CAST(data AS TEXT), '$.seq'), 0)",
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// probeWrite performs a real write and then rolls it back. Reopening an
// existing database writes nothing (the schema is already there and WAL is
// already on), and SQLite opens a file it cannot write as read-only, without
// an error; without the probe, the failure would only show up on the first
// exchange.
func probeWrite(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec("CREATE TABLE gateway_write_probe (x INTEGER)")
	return err
}

func (s *SQLite) Record(ctx context.Context, e *exchange.Exchange) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	intervened := 0
	if e.Intervened() {
		intervened = 1
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO exchanges
		(id, route, upstream, override, method, path, status, intervened, start_sec, start_nsec, seq, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Route, e.Upstream, e.Override, e.Method, []byte(e.Path), e.Status, intervened,
		e.Start.Unix(), e.Start.Nanosecond(), int64(e.Seq), data)
	if se := (*sqlite.Error)(nil); errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrDuplicateID
	}
	if err != nil {
		return fmt.Errorf("writing history to %s: %w", s.path, err)
	}
	return nil
}

// where translates the filter into SQL, clause by clause of exchange.Filter.Match.
func where(f exchange.Filter) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, a ...any) {
		conds = append(conds, cond)
		args = append(args, a...)
	}
	if f.Route != "" {
		add("route = ?", f.Route)
	}
	if f.Upstream != "" {
		add("upstream = ?", f.Upstream)
	}
	if f.Override != "" {
		add("override = ?", f.Override)
	}
	if f.Method != "" {
		add(foldEqual+"(method, ?)", f.Method)
	}
	if f.Path != "" {
		add("instr(path, ?) > 0", []byte(f.Path))
	}
	if f.StatusMin != 0 {
		add("status >= ?", f.StatusMin)
	}
	if f.StatusMax != 0 {
		add("status <= ?", f.StatusMax)
	}
	if f.Intervened != nil {
		v := 0
		if *f.Intervened {
			v = 1
		}
		add("intervened = ?", v)
	}
	if !f.Since.IsZero() {
		// Not before Since: start >= Since.
		s, n := f.Since.Unix(), f.Since.Nanosecond()
		add("(start_sec > ? OR (start_sec = ? AND start_nsec >= ?))", s, s, n)
	}
	if !f.Until.IsZero() {
		// Before Until: start < Until.
		s, n := f.Until.Unix(), f.Until.Nanosecond()
		add("(start_sec < ? OR (start_sec = ? AND start_nsec < ?))", s, s, n)
	}
	if len(conds) == 0 {
		return "1", nil
	}
	return strings.Join(conds, " AND "), args
}

func decode(data []byte) (exchange.Exchange, error) {
	var e exchange.Exchange
	if err := json.Unmarshal(data, &e); err != nil {
		return exchange.Exchange{}, fmt.Errorf("unreadable exchange in the history: %w", err)
	}
	return e, nil
}

// epoch reads the history epoch: how many times the database has been cleared.
func epoch(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (uint64, error) {
	var v int64
	err := q.QueryRowContext(ctx, "SELECT v FROM gateway_meta WHERE k = 'epoch'").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return uint64(v), err
}

func (s *SQLite) List(ctx context.Context, f exchange.Filter, p Page) (ListResult, error) {
	limit := NormalizeLimit(p.Limit)
	var c cursor
	if p.Cursor != "" {
		var err error
		if c, err = decodeCursor(p.Cursor); err != nil {
			return ListResult{}, err
		}
	}
	// The epoch and the exchanges are read in the same transaction, so that a
	// clear in between cannot produce a cursor with the wrong epoch.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ListResult{}, fmt.Errorf("querying history at %s: %w", s.path, err)
	}
	defer tx.Rollback()
	ep, err := epoch(ctx, tx)
	if err != nil {
		return ListResult{}, fmt.Errorf("querying history at %s: %w", s.path, err)
	}
	cond, args := where(f)
	if p.Cursor != "" {
		if c.Epoch != ep {
			return ListResult{}, nil // cursor predates the last clear
		}
		// The cursor is the key of the last exchange delivered; the listing
		// resumes from the one before it.
		cond = orderCols + " < (?, ?, ?, ?) AND " + cond
		args = append([]any{c.Key.Sec, c.Key.Nsec, int64(c.Key.Seq), int64(c.Key.Ins)}, args...)
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT start_sec, start_nsec, seq, ord, data FROM exchanges WHERE "+cond+
			" ORDER BY start_sec DESC, start_nsec DESC, seq DESC, ord DESC LIMIT ?",
		append(args, limit+1)...)
	if err != nil {
		return ListResult{}, fmt.Errorf("querying history at %s: %w", s.path, err)
	}
	defer rows.Close()
	var res ListResult
	var last orderKey
	for rows.Next() {
		if len(res.Items) == limit {
			// One more exchange matches: the page continues after the last one delivered.
			res.Next = encodeCursor(ep, last)
			break
		}
		var data []byte
		var seq, ord int64
		if err := rows.Scan(&last.Sec, &last.Nsec, &seq, &ord, &data); err != nil {
			return ListResult{}, err
		}
		last.Seq, last.Ins = uint64(seq), uint64(ord)
		e, err := decode(data)
		if err != nil {
			return ListResult{}, err
		}
		res.Items = append(res.Items, e)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	return res, nil
}

func (s *SQLite) Get(ctx context.Context, id string) (exchange.Exchange, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM exchanges WHERE id = ?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return exchange.Exchange{}, ErrNotFound
	}
	if err != nil {
		return exchange.Exchange{}, fmt.Errorf("reading history from %s: %w", s.path, err)
	}
	return decode(data)
}

func (s *SQLite) Neighbor(ctx context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	var sec, nsec, seq, ord int64
	err := s.db.QueryRowContext(ctx, "SELECT start_sec, start_nsec, seq, ord FROM exchanges WHERE id = ?", id).
		Scan(&sec, &nsec, &seq, &ord)
	if errors.Is(err, sql.ErrNoRows) {
		return exchange.Exchange{}, ErrNotFound
	}
	if err != nil {
		return exchange.Exchange{}, fmt.Errorf("reading history from %s: %w", s.path, err)
	}
	cmp, order := "<", "DESC"
	if d == Newer {
		cmp, order = ">", "ASC"
	}
	cond, args := where(f)
	var data []byte
	err = s.db.QueryRowContext(ctx,
		"SELECT data FROM exchanges WHERE "+orderCols+" "+cmp+" (?, ?, ?, ?) AND "+cond+
			" ORDER BY start_sec "+order+", start_nsec "+order+", seq "+order+", ord "+order+" LIMIT 1",
		append([]any{sec, nsec, seq, ord}, args...)...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return exchange.Exchange{}, ErrNoMore
	}
	if err != nil {
		return exchange.Exchange{}, fmt.Errorf("reading history from %s: %w", s.path, err)
	}
	return decode(data)
}

// Clear deletes the exchanges and bumps the epoch, in the same transaction.
func (s *SQLite) Clear(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("clearing history at %s: %w", s.path, err)
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		"DELETE FROM exchanges",
		"INSERT INTO gateway_meta (k, v) VALUES ('epoch', 1) ON CONFLICT(k) DO UPDATE SET v = v + 1",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("clearing history at %s: %w", s.path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clearing history at %s: %w", s.path, err)
	}
	return nil
}

// Path is the database file.
func (s *SQLite) Path() string { return s.path }

func (s *SQLite) Close() error { return s.db.Close() }

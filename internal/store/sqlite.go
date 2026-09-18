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
	"strconv"
	"strings"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// foldEqual é strings.EqualFold exposto ao SQL. O COLLATE NOCASE do SQLite
// só dobra ASCII e divergiria de exchange.Filter.Match no filtro de método.
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

// sqliteSchema guarda cada troca completa em JSON, ao lado das colunas usadas
// nos filtros. A chave ord é a ordem do histórico e o cursor de paginação;
// AUTOINCREMENT impede que uma limpeza reaproveite chaves já emitidas.
//
// O início fica em segundos e nanossegundos separados, e não em nanossegundos
// desde a época, porque UnixNano não representa instantes fora de 1678–2262
// (entre eles o instante zero) e a janela de tempo precisa comparar como
// time.Time.Before. O path fica como BLOB para que instr compare bytes, como
// strings.Contains.
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
	data       BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS exchanges_route ON exchanges(route);
CREATE INDEX IF NOT EXISTS exchanges_status ON exchanges(status);
CREATE INDEX IF NOT EXISTS exchanges_start ON exchanges(start_sec, start_nsec);
`

// SQLite guarda o histórico num banco SQLite local, pelo driver puro em Go
// (modernc.org/sqlite), para que o binário continue compilando sem CGO. Os
// filtros rodam em SQL e reproduzem exchange.Filter.Match.
type SQLite struct {
	db   *sql.DB
	path string
}

// OpenSQLite abre (ou cria) o banco em path e garante o esquema. A abertura
// só conclui depois de uma escrita real, para que um caminho sem permissão
// falhe aqui e não na primeira troca.
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
	// Uma conexão só: o SQLite serializa escritas de qualquer forma, e assim
	// escritas concorrentes esperam na fila do database/sql em vez de falhar
	// com SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=5000", sqliteSchema} {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := probeWrite(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("o banco não aceita escrita: %w", err)
	}
	return &SQLite{db: db, path: path}, nil
}

// probeWrite faz uma escrita real, desfeita em seguida. Reabrir um banco
// existente não escreve nada (o esquema já existe e o WAL já está ligado),
// e o SQLite abre só para leitura, sem erro, um arquivo que não pode ser
// escrito; sem a sonda, a falha só apareceria na primeira troca.
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
		(id, route, upstream, override, method, path, status, intervened, start_sec, start_nsec, data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Route, e.Upstream, e.Override, e.Method, []byte(e.Path), e.Status, intervened,
		e.Start.Unix(), e.Start.Nanosecond(), data)
	if se := (*sqlite.Error)(nil); errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
		return ErrDuplicateID
	}
	if err != nil {
		return fmt.Errorf("gravando histórico em %s: %w", s.path, err)
	}
	return nil
}

// where traduz o filtro para SQL, cláusula a cláusula de exchange.Filter.Match.
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
		// Não anterior a Since: start >= Since.
		s, n := f.Since.Unix(), f.Since.Nanosecond()
		add("(start_sec > ? OR (start_sec = ? AND start_nsec >= ?))", s, s, n)
	}
	if !f.Until.IsZero() {
		// Anterior a Until: start < Until.
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
		return exchange.Exchange{}, fmt.Errorf("troca ilegível no histórico: %w", err)
	}
	return e, nil
}

func (s *SQLite) List(ctx context.Context, f exchange.Filter, p Page) (ListResult, error) {
	limit := NormalizeLimit(p.Limit)
	cond, args := where(f)
	if p.Cursor != "" {
		c, err := strconv.ParseUint(p.Cursor, 10, 63)
		if err != nil {
			return ListResult{}, ErrBadCursor
		}
		// O cursor é a chave da última troca entregue; segue-se da anterior.
		cond = "ord < ? AND " + cond
		args = append([]any{int64(c)}, args...)
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT ord, data FROM exchanges WHERE "+cond+" ORDER BY ord DESC LIMIT ?",
		append(args, limit+1)...)
	if err != nil {
		return ListResult{}, fmt.Errorf("consultando histórico em %s: %w", s.path, err)
	}
	defer rows.Close()
	var res ListResult
	var last int64
	for rows.Next() {
		if len(res.Items) == limit {
			// Há mais uma troca que casa: a página continua depois da última entregue.
			res.Next = strconv.FormatInt(last, 10)
			break
		}
		var data []byte
		if err := rows.Scan(&last, &data); err != nil {
			return ListResult{}, err
		}
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
		return exchange.Exchange{}, fmt.Errorf("lendo histórico em %s: %w", s.path, err)
	}
	return decode(data)
}

func (s *SQLite) Neighbor(ctx context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	var ord int64
	err := s.db.QueryRowContext(ctx, "SELECT ord FROM exchanges WHERE id = ?", id).Scan(&ord)
	if errors.Is(err, sql.ErrNoRows) {
		return exchange.Exchange{}, ErrNotFound
	}
	if err != nil {
		return exchange.Exchange{}, fmt.Errorf("lendo histórico em %s: %w", s.path, err)
	}
	cmp, order := "<", "DESC"
	if d == Newer {
		cmp, order = ">", "ASC"
	}
	cond, args := where(f)
	var data []byte
	err = s.db.QueryRowContext(ctx,
		"SELECT data FROM exchanges WHERE ord "+cmp+" ? AND "+cond+" ORDER BY ord "+order+" LIMIT 1",
		append([]any{ord}, args...)...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return exchange.Exchange{}, ErrNoMore
	}
	if err != nil {
		return exchange.Exchange{}, fmt.Errorf("lendo histórico em %s: %w", s.path, err)
	}
	return decode(data)
}

func (s *SQLite) Clear(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM exchanges"); err != nil {
		return fmt.Errorf("limpando histórico em %s: %w", s.path, err)
	}
	return nil
}

// Path é o arquivo do banco.
func (s *SQLite) Path() string { return s.path }

func (s *SQLite) Close() error { return s.db.Close() }

// Package store define o armazenamento do histórico de trocas e suas
// implementações: memória, arquivo NDJSON e SQLite.
package store

import (
	"context"
	"errors"

	"github.com/gamerjp64/gateway/internal/exchange"
)

var (
	// ErrNotFound: nenhuma troca com o identificador pedido.
	ErrNotFound = errors.New("troca não encontrada")
	// ErrNoMore: a navegação chegou ao fim do histórico nessa direção.
	ErrNoMore = errors.New("não há mais trocas nessa direção")
	// ErrBadCursor: o cursor de paginação não foi emitido por este store.
	ErrBadCursor = errors.New("cursor de paginação inválido")
	// ErrDuplicateID: já existe no histórico uma troca com o identificador
	// que se tentou registrar; o registro é recusado e nada muda.
	ErrDuplicateID = errors.New("já existe uma troca com esse identificador")
)

// Direction orienta a navegação item a item.
type Direction int

const (
	// Older anda para trás no tempo.
	Older Direction = iota
	// Newer anda para frente no tempo.
	Newer
)

// Page pede uma página da listagem. Cursor vazio começa pela troca mais nova.
type Page struct {
	Limit  int
	Cursor string
}

// ListResult é uma página, da troca mais nova para a mais antiga. Next vazio
// indica que não há continuação.
type ListResult struct {
	Items []exchange.Exchange
	Next  string
}

// Store guarda o histórico. A ordem do histórico é a ordem de registro, e
// todas as implementações precisam passar pela mesma bateria de contrato
// (pacote storetest).
type Store interface {
	// Record acrescenta a troca ao histórico, ou devolve ErrDuplicateID se
	// o identificador já constar dele.
	Record(ctx context.Context, e *exchange.Exchange) error
	List(ctx context.Context, f exchange.Filter, p Page) (ListResult, error)
	Get(ctx context.Context, id string) (exchange.Exchange, error)
	// Neighbor devolve a troca vizinha de id na direção pedida, entre as que
	// satisfazem o filtro. A própria troca id não precisa satisfazê-lo.
	Neighbor(ctx context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error)
	Clear(ctx context.Context) error
	Close() error
}

// DefaultLimit e MaxLimit delimitam o tamanho de página.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// NormalizeLimit aplica o padrão e o teto de página.
func NormalizeLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}

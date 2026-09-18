package store

import (
	"cmp"
	"encoding/base64"
	"encoding/json"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// orderKey posiciona uma troca no histórico. A ordem é cronológica pela
// chegada: instante de início, depois o número de sequência de chegada e,
// por último, a ordem de registro no backend, que só desempata trocas com
// início e sequência iguais (como as de processos diferentes no mesmo
// instante). A captura grava a troca quando ela termina; sem esta chave,
// uma troca lenta que chegou antes apareceria como mais nova que as rápidas
// que chegaram depois dela.
//
// O início fica em segundos e nanossegundos separados, e não em
// nanossegundos desde a época, porque UnixNano não representa instantes fora
// de 1678–2262 (entre eles o instante zero).
type orderKey struct {
	Sec  int64  `json:"s"`
	Nsec int32  `json:"n"`
	Seq  uint64 `json:"q"`
	Ins  uint64 `json:"i"`
}

// keyOf monta a chave de e, registrada na posição de registro ins.
func keyOf(e *exchange.Exchange, ins uint64) orderKey {
	return orderKey{Sec: e.Start.Unix(), Nsec: int32(e.Start.Nanosecond()), Seq: e.Seq, Ins: ins}
}

// compare devolve -1, 0 ou +1 conforme a esteja antes, junto ou depois de b.
func (a orderKey) compare(b orderKey) int {
	if c := cmp.Compare(a.Sec, b.Sec); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Nsec, b.Nsec); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Seq, b.Seq); c != 0 {
		return c
	}
	return cmp.Compare(a.Ins, b.Ins)
}

// cursor é a continuação de uma listagem: a chave da última troca entregue
// e a época do histórico em que ela foi emitida. Cada limpeza avança a
// época, e um cursor de época anterior não alcança as trocas novas.
type cursor struct {
	Epoch uint64   `json:"e"`
	Key   orderKey `json:"k"`
}

// encodeCursor serializa o cursor num texto opaco, seguro em URL.
func encodeCursor(epoch uint64, k orderKey) string {
	b, _ := json.Marshal(cursor{Epoch: epoch, Key: k})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor lê um cursor emitido por encodeCursor, ou devolve
// ErrBadCursor.
func decodeCursor(s string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, ErrBadCursor
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return cursor{}, ErrBadCursor
	}
	return c, nil
}

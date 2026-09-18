package writer

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// No Windows, substituir ou remover um arquivo que outro processo mantém
// aberto (um editor, o antivírus indexando o diretório) falha com acesso
// negado ou violação de compartilhamento até o outro lado soltá-lo. Essas
// falhas são transitórias: a operação é repetida por um intervalo curto.
const retryFor = 500 * time.Millisecond

func transient(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	const errorSharingViolation syscall.Errno = 32
	return errno == syscall.ERROR_ACCESS_DENIED || errno == errorSharingViolation
}

func retry(op func() error) error {
	deadline := time.Now().Add(retryFor)
	wait := time.Millisecond
	for {
		err := op()
		if err == nil || !transient(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(wait)
		wait = min(2*wait, 50*time.Millisecond)
	}
}

func rename(from, to string) error { return retry(func() error { return os.Rename(from, to) }) }

func remove(path string) error { return retry(func() error { return os.Remove(path) }) }

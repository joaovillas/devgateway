package writer

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// On Windows, replacing or removing a file that another process holds open
// (an editor, the antivirus indexing the directory) fails with access denied
// or a sharing violation until the other side lets go. Those failures are
// transient: the operation is retried for a short while.
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

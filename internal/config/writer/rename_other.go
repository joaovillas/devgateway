//go:build !windows

package writer

import "os"

func rename(from, to string) error { return os.Rename(from, to) }

func remove(path string) error { return os.Remove(path) }

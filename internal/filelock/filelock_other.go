//go:build !windows && !unix

package filelock

import "os"

// tryLock is a no-op where no file locking is available.
func tryLock(*os.File) (bool, error) { return false, nil }

func release(*os.File) {}

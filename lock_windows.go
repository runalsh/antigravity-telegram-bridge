//go:build windows

package main

import (
	"os"
)

func acquireLock(f *os.File) error {
	return nil
}

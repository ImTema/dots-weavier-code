//go:build unix

package main

import (
	"fmt"
	"runtime"
	"syscall"
)

// maxRSS is the peak resident memory of this process, for the DOTWEAVIER_DEBUG line.
func maxRSS() string {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return ""
	}
	b := int64(ru.Maxrss)
	if runtime.GOOS != "darwin" { // Linux and the BSDs report kilobytes, darwin bytes
		b <<= 10
	}
	return fmt.Sprintf(" maxRSS=%dMB", b>>20)
}

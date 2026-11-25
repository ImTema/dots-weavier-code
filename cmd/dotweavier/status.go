package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// status redraws label, phase and elapsed time on one stderr line until done prints the summary, the only line off a terminal.
// shortcut: concurrent requests in the overview server share the line and overwrite each other.
func status(label string) (phase func(string), done func(format string, a ...any)) {
	fi, err := os.Stderr.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	t0 := time.Now()
	var mu sync.Mutex
	cur := ""
	draw := func() {
		fmt.Fprintf(os.Stderr, "\r\033[K%s: %s %s", label, cur, time.Since(t0).Round(100*time.Millisecond))
	}
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		if !tty {
			return
		}
		tk := time.NewTicker(200 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				mu.Lock()
				draw()
				mu.Unlock()
			}
		}
	}()
	phase = func(p string) {
		mu.Lock()
		defer mu.Unlock()
		cur = p
		if tty {
			draw()
		}
	}
	done = func(format string, a ...any) {
		close(stop)
		<-finished
		if tty {
			fmt.Fprint(os.Stderr, "\r\033[K")
		}
		fmt.Fprintf(os.Stderr, "%s: %s (%s)\n", label, fmt.Sprintf(format, a...), time.Since(t0).Round(time.Millisecond))
	}
	return phase, done
}

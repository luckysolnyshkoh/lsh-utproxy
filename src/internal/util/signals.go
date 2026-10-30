package util

import (
	"context"
	"os"
	"os/signal"
)

// ContextWithSignals returns a context that is canceled when the process is
// asked to stop.
//
// On Linux/Unix that is SIGINT, SIGTERM or SIGHUP. On Windows a console run is
// stopped with Ctrl+C, and a service run is stopped by the Service Control
// Manager, which calls the returned cancel function directly.
func ContextWithSignals() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, stopSignals()...)
	go func() {
		select {
		case <-sig:
			cancel()
		case <-ctx.Done():
		}
		signal.Stop(sig)
	}()
	return ctx, cancel
}

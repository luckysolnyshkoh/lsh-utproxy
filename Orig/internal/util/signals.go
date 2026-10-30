package util

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func ContextWithSignals() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		cancel()
	}()
	return ctx
}
package proxy

import (
	"context"
	"errors"
	"fmt"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type FrontendWorker struct {
	fe     config.Frontend
	cfg    *config.Config
	st     *stats.Stats
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func Run(cfg *config.Config) error {
	if cfg.Global.MaxProcs > 0 {
		runtime.GOMAXPROCS(cfg.Global.MaxProcs)
	} else {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}
	initLogger(cfg)
	ctx := util.ContextWithSignals()
	st := stats.New()
	var wg sync.WaitGroup

	backendMap := make(map[string]*BackendSet)
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		slist := make([]balancer.Server, 0, len(b.Servers))
		bsList := make([]*BackendServer, 0, len(b.Servers))
		for j := range b.Servers {
			sc := &b.Servers[j]
			bsrv := NewBackendServer(*sc, b)
			slist = append(slist, bsrv)
			bsList = append(bsList, bsrv)
		}
		mode := b.Mode
		if mode == "" {
			mode = cfg.Defaults.Mode
		}
		if mode == "" {
			mode = "tcp"
		}
		bset := NewBackendSet(b, slist, mode)
		backendMap[b.Name] = bset
		wg.Add(1)
		go func(m string, list []*BackendServer) {
			defer wg.Done()
			healthCheckLoop(ctx, list, m)
		}(mode, bsList)
	}

	for i := range cfg.Frontends {
		fe := &cfg.Frontends[i]
		fctx, fcancel := context.WithCancel(ctx)
		fw := &FrontendWorker{fe: *fe, cfg: cfg, st: st, ctx: fctx, cancel: fcancel}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fw.Run(backendMap); err != nil && !errors.Is(err, context.Canceled) {
				log.Error().Err(err).Str("frontend", fw.fe.Name).Msg("frontend stopped with error")
			}
		}()
	}

	<-ctx.Done()
	log.Info().Dur("grace", cfg.Global.GraceShutdown).Msg("shutdown signal received, draining...")
	shutdownCtx, sc := context.WithTimeout(context.Background(), cfg.Global.GraceShutdown)
	_ = shutdownCtx
	sc()

	wg.Wait()
	log.Info().Msg("lsh-utproxy stopped")
	return nil
}

func initLogger(cfg *config.Config) {
	lvl := zerolog.InfoLevel
	switch cfg.Global.LogLevel {
	case "debug":
		lvl = zerolog.DebugLevel
	case "info":
		lvl = zerolog.InfoLevel
	case "warn":
		lvl = zerolog.WarnLevel
	case "error":
		lvl = zerolog.ErrorLevel
	case "none":
		lvl = zerolog.Disabled
	}
	zerolog.SetGlobalLevel(lvl)
	if cfg.Global.LogFormat == "json" {
		log.Logger = zerolog.New(zerolog.NewConsoleWriter()).With().Timestamp().Logger()
	} else {
		log.Logger = zerolog.New(zerolog.NewConsoleWriter(func(w *zerolog.ConsoleWriter) { w.TimeFormat = time.RFC3339 })).With().Timestamp().Logger()
	}
}

func (fw *FrontendWorker) Run(backendMap map[string]*BackendSet) error {
	fe := fw.fe
	mode := fe.Mode
	if mode == "" {
		mode = fw.cfg.Defaults.Mode
	}
	if mode == "" {
		mode = "tcp"
	}
	for _, b := range fe.Bind {
		addr := b.Addr
		if addr == "" || addr == "*" {
			addr = fmt.Sprintf("0.0.0.0:%d", b.Port)
		}
		if strings.HasPrefix(addr, "*:") {
			addr = "0.0.0.0:" + addr[2:]
		}
		if mode == "udp" {
			fw.wg.Add(1)
			go func(a string) {
				defer fw.wg.Done()
				if err := startUDP(fw.ctx, fw.cfg, fe, a, backendMap, fw.st); err != nil && !errors.Is(err, context.Canceled) {
					log.Error().Err(err).Str("frontend", fe.Name).Str("addr", a).Msg("udp listener error")
				}
			}(addr)
		} else {
			fw.wg.Add(1)
			go func(a string) {
				defer fw.wg.Done()
				if err := startTCP(fw.ctx, fw.cfg, fe, a, backendMap, fw.st); err != nil && !errors.Is(err, context.Canceled) {
					log.Error().Err(err).Str("frontend", fe.Name).Str("addr", a).Msg("tcp listener error")
				}
			}(addr)
		}
	}
	fw.wg.Wait()
	return nil
}
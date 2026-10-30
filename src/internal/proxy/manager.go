package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"sync"
	"time"

	"lsh-utproxy/internal/acl"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/logging"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"

	"github.com/rs/zerolog/log"
)

// frontendState keeps one frontend's mode, its own maxconn counter and the
// sockets already bound for it.
type frontendState struct {
	fe    config.Frontend
	mode  string
	acl   *acl.List
	feLim *connLimiter
	tcp   []net.Listener
	udp   []*net.UDPConn
}

func (f *frontendState) closeAll() {
	for _, ln := range f.tcp {
		_ = ln.Close()
	}
	for _, c := range f.udp {
		_ = c.Close()
	}
}

// Run starts the proxy with the process signal context (Ctrl+C on Windows and
// Linux, SIGTERM/SIGHUP on Unix). It is the entry point for a console run.
func Run(cfg *config.Config) error {
	ctx, cancel := util.ContextWithSignals()
	defer cancel()
	return RunWithCtx(ctx, cfg)
}

// RunWithCtx runs the proxy until ctx is canceled: a Windows service passes a
// context stopped by the Service Control Manager, systemd uses SIGTERM.
func RunWithCtx(ctx context.Context, cfg *config.Config) error {
	logging.Setup(cfg)

	if cfg.Global.MaxProcs > 0 {
		runtime.GOMAXPROCS(cfg.Global.MaxProcs)
	} else {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}
	if cfg.Global.Daemon {
		log.Warn().Msg("'daemon' is ignored: run the binary as a systemd unit (Linux) or a Windows service")
	}
	st := stats.New()

	if cur, changed := util.RaiseFDLimit(fdLimitTarget(cfg)); changed {
		log.Info().Int("rlimit_nofile", cur).Msg("raised the open file (socket) limit")
	}
	if cfg.Defaults.ReUsePort && !util.ReusePortSupported {
		log.Warn().Msg("re-use-port (SO_REUSEPORT) is not supported on this OS; listeners use a plain bind")
	}

	backendMap := make(map[string]*BackendSet)
	for i := range cfg.Backends {
		b := &cfg.Backends[i]
		slist := make([]balancer.Server, 0, len(b.Servers))
		for j := range b.Servers {
			sc := &b.Servers[j]
			slist = append(slist, NewBackendServer(*sc, b))
		}
		bset := NewBackendSet(b, slist, resolveMode(cfg, b.Mode))
		backendMap[b.Name] = bset
		if len(slist) == 0 {
			log.Warn().Str("backend", b.Name).Msg("backend has no servers")
		}
	}

	globLim := &connLimiter{limit: cfg.Global.MaxConn}

	// Phase 1: bind every listen address before serving. A bad bind must stop
	// the process with a clear error instead of leaving a half-started proxy.
	frontends := make([]*frontendState, 0, len(cfg.Frontends))
	for i := range cfg.Frontends {
		fe := cfg.Frontends[i]
		aclList, err := acl.New(fe.AccessRules)
		if err != nil {
			closeFrontends(frontends)
			return fmt.Errorf("frontend %s: access rules: %w", fe.Name, err)
		}
		fs := &frontendState{fe: fe, mode: resolveMode(cfg, fe.Mode), acl: aclList, feLim: &connLimiter{limit: fe.MaxConn}}
		if aclList.Len() > 0 {
			log.Info().Str("frontend", fe.Name).Int("access_rules", aclList.Len()).
				Msg("access rules loaded, first matching rule wins")
		}
		if _, ok := backendMap[fe.DefaultBackend]; !ok {
			log.Warn().Str("frontend", fe.Name).Str("backend", fe.DefaultBackend).
				Msg("frontend default_backend is not defined, connections to this frontend will be closed")
		}
		if bset, ok := backendMap[fe.DefaultBackend]; ok && bset.mode != fs.mode {
			log.Warn().Str("frontend", fe.Name).Str("mode", fs.mode).
				Str("backend", fe.DefaultBackend).Str("backend_mode", bset.mode).
				Msg("frontend and backend modes differ; the frontend mode decides how the traffic is handled")
		}
		for _, b := range fe.Bind {
			addr := util.NormalizeBind(b.Addr)
			if fs.mode == "udp" {
				c, err := listenUDP(cfg, addr)
				if err != nil {
					closeFrontends(frontends)
					fs.closeAll()
					return fmt.Errorf("frontend %s: bind udp %s: %w", fe.Name, addr, err)
				}
				fs.udp = append(fs.udp, c)
			} else {
				ln, err := listenTCP(cfg, addr)
				if err != nil {
					closeFrontends(frontends)
					fs.closeAll()
					return fmt.Errorf("frontend %s: bind tcp %s: %w", fe.Name, addr, err)
				}
				fs.tcp = append(fs.tcp, ln)
			}
			log.Info().Str("frontend", fe.Name).Str("mode", fs.mode).Str("addr", addr).Msg("listening")
		}
		frontends = append(frontends, fs)
	}

	// Phase 2: drop privileges. This happens after binding, so ports below 1024
	// still work when the process starts as root, while serving runs unprivileged.
	if err := util.ApplyPrivileges(cfg.Global.Chroot, cfg.Global.User, cfg.Global.Group); err != nil {
		closeFrontends(frontends)
		return fmt.Errorf("privileges: %w", err)
	}

	// Phase 3: serve.
	var wg sync.WaitGroup
	for name, bset := range backendMap {
		list := make([]*BackendServer, 0, len(bset.srvs))
		for _, s := range bset.srvs {
			if bs, ok := s.(*BackendServer); ok {
				list = append(list, bs)
			}
		}
		wg.Add(1)
		go func(m string, servers []*BackendServer, bname string) {
			defer wg.Done()
			healthCheckLoop(ctx, servers, m)
		}(bset.mode, list, name)
	}

	for _, f := range frontends {
		for _, ln := range f.tcp {
			wg.Add(1)
			go func(fe *frontendState, l net.Listener, addr string) {
				defer wg.Done()
				if err := serveTCP(ctx, cfg, fe.fe, fe.acl, l, addr, backendMap, st, fe.feLim, globLim); err != nil && !errors.Is(err, context.Canceled) {
					log.Error().Err(err).Str("frontend", fe.fe.Name).Str("addr", addr).Msg("tcp listener error")
				}
			}(f, ln, addrOf(ln))
		}
		for _, pc := range f.udp {
			wg.Add(1)
			go func(fe *frontendState, c *net.UDPConn, addr string) {
				defer wg.Done()
				if err := serveUDP(ctx, cfg, fe.fe, fe.acl, c, backendMap, st); err != nil && !errors.Is(err, context.Canceled) {
					log.Error().Err(err).Str("frontend", fe.fe.Name).Str("addr", addr).Msg("udp listener error")
				}
			}(f, pc, pc.LocalAddr().String())
		}
	}

	log.Info().
		Int("frontends", len(frontends)).
		Int("backends", len(backendMap)).
		Int("max_conn", cfg.Global.MaxConn).
		Msg("lsh-utproxy started")

	<-ctx.Done()
	log.Info().Dur("grace", cfg.Global.GraceShutdown).Msg("shutdown requested, draining connections")

	// The listeners are closed by the serve loops, so no new session starts.
	// Established sessions may run to completion; after the grace window we stop
	// waiting and let the process exit, which closes the remaining sockets.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	grace := cfg.Global.GraceShutdown
	if grace <= 0 {
		grace = 10 * time.Second
	}
	select {
	case <-done:
	case <-time.After(grace):
		log.Warn().Dur("grace", grace).Msg("graceful shutdown deadline exceeded, closing remaining connections")
	}
	log.Info().Msg("lsh-utproxy stopped")
	return nil
}

func closeFrontends(fs []*frontendState) {
	for _, f := range fs {
		f.closeAll()
	}
}

func addrOf(ln net.Listener) string {
	if ln == nil || ln.Addr() == nil {
		return ""
	}
	return ln.Addr().String()
}

// resolveMode applies the section mode, then the defaults, then tcp.
func resolveMode(cfg *config.Config, mode string) string {
	if mode == "" {
		mode = cfg.Defaults.Mode
	}
	if mode == "" {
		mode = "tcp"
	}
	return mode
}

// fdLimitTarget estimates how many descriptors the process needs: one socket
// per accepted connection, one toward the backend, plus headroom.
func fdLimitTarget(cfg *config.Config) int {
	if cfg.Global.MaxConn > 0 {
		return cfg.Global.MaxConn*2 + 1024
	}
	return 0 // raise to the hard limit
}

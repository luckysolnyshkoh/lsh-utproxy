package proxy

import (
	"context"
	"errors"
	"io"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

func startTCP(ctx context.Context, cfg *config.Config, fe config.Frontend, addr string, backendMap map[string]*BackendSet, st *stats.Stats) error {
	var ln net.Listener
	var err error
	reuse := cfg.Defaults.ReUsePort
	if reuse {
		ln, err = util.ListenTCPReuse(addr)
	} else {
		ln, err = net.Listen("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer ln.Close()
	log.Info().Str("frontend", fe.Name).Str("mode", "tcp").Str("addr", addr).Msg("listening")
	var wg sync.WaitGroup
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		default:
		}
		conn, err := ln.Accept()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if errors.Is(err, io.EOF) {
				continue
			}
			return err
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer c.Close()
			handleTCP(ctx, cfg, fe, c, backendMap, st)
		}(conn)
	}
}

func handleTCP(ctx context.Context, cfg *config.Config, fe config.Frontend, cli net.Conn, backendMap map[string]*BackendSet, st *stats.Stats) {
	st.ConnsTotal.Add(1)
	st.ConnsOpen.Add(1)
	defer st.ConnsOpen.Add(-1)

	bn := fe.DefaultBackend
	bset, ok := backendMap[bn]
	if !ok || bset == nil {
		log.Warn().Str("frontend", fe.Name).Str("backend", bn).Msg("backend not found")
		return
	}
	if bset.mode == "" {
		bset.mode = "tcp"
	}

	var bsrv balancer.Server
	var berr error
	for i := 0; i < 2; i++ {
		bsrv, berr = bset.Next()
		if berr == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if berr != nil {
		st.Errors.Add(1)
		log.Debug().Err(berr).Msg("no backend server")
		return
	}
	bsrvS, ok2 := bsrv.(*BackendServer)
	if !ok2 {
		st.Errors.Add(1)
		return
	}
	if bsrvS.MaxConn() > 0 && bsrvS.Active() >= int64(bsrvS.MaxConn()) {
		st.Dropped.Add(1)
		return
	}
	bsrvS.Inc()
	defer bsrvS.Dec()

	d := net.Dialer{Timeout: getTO(cfg, fe, bset, "connect")}
	beConn, err := d.DialContext(ctx, "tcp", bsrvS.Addr())
	if err != nil {
		st.Errors.Add(1)
		log.Debug().Err(err).Str("backend", bn).Str("server", bsrvS.ID()).Str("addr", bsrvS.Addr()).Msg("backend dial failed")
		return
	}
	defer beConn.Close()

	if getBool(cfg, fe, bset, "tcpka") {
		if tc, ok := beConn.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
			idle := getDur(cfg, fe, bset, "ka_idle")
			if idle > 0 {
				_ = tc.SetKeepAlivePeriod(idle)
			}
		}
		if tc, ok := cli.(*net.TCPConn); ok {
			_ = tc.SetKeepAlive(true)
		}
	}

	buf := util.GetBuf()
	defer util.PutBuf(buf)
	_, _, e1, e2 := copyBidir(ctx, beConn, cli, buf, st, getTO(cfg, fe, bset, "idle"))
	if e1 != nil && !isCloseErr(e1) {
		log.Debug().Err(e1).Msg("tcp proxy err")
	}
	if e2 != nil && !isCloseErr(e2) {
		log.Debug().Err(e2).Msg("tcp proxy err")
	}
}

func getTO(cfg *config.Config, fe config.Frontend, bset *BackendSet, k string) time.Duration {
	switch k {
	case "connect":
		if bset != nil && bset.bcfg.TimeoutConnect > 0 {
			return bset.bcfg.TimeoutConnect
		}
		if cfg.Defaults.TimeoutConnect > 0 {
			return cfg.Defaults.TimeoutConnect
		}
		return 5 * time.Second
	case "idle":
		if bset != nil && bset.bcfg.TimeoutIdle > 0 {
			return bset.bcfg.TimeoutIdle
		}
		if fe.TimeoutIdle > 0 {
			return fe.TimeoutIdle
		}
		if cfg.Defaults.TimeoutIdle > 0 {
			return cfg.Defaults.TimeoutIdle
		}
		return 60 * time.Second
	case "server":
		if bset != nil && bset.bcfg.TimeoutServer > 0 {
			return bset.bcfg.TimeoutServer
		}
		if cfg.Defaults.TimeoutServer > 0 {
			return cfg.Defaults.TimeoutServer
		}
		return 30 * time.Second
	case "client":
		if fe.TimeoutClient > 0 {
			return fe.TimeoutClient
		}
		if cfg.Defaults.TimeoutClient > 0 {
			return cfg.Defaults.TimeoutClient
		}
		return 30 * time.Second
	case "tunnel":
		if fe.TimeoutTunnel > 0 {
			return fe.TimeoutTunnel
		}
		if bset != nil && bset.bcfg.TimeoutTunnel > 0 {
			return bset.bcfg.TimeoutTunnel
		}
		if cfg.Defaults.TimeoutTunnel > 0 {
			return cfg.Defaults.TimeoutTunnel
		}
		return 1 * time.Hour
	default:
		return 0
	}
}

func getBool(cfg *config.Config, fe config.Frontend, bset *BackendSet, k string) bool {
	switch k {
	case "tcpka":
		if bset != nil && bset.bcfg.OptionTCPKeepAlive {
			return true
		}
		if fe.OptionTCPKeepAlive {
			return true
		}
		return cfg.Defaults.OptionTCPKeepAlive
	default:
		return false
	}
}

func getDur(cfg *config.Config, fe config.Frontend, bset *BackendSet, k string) time.Duration {
	switch k {
	case "ka_idle":
		if bset != nil && bset.bcfg.TCPKeepAliveIdle > 0 {
			return bset.bcfg.TCPKeepAliveIdle
		}
		if cfg.Defaults.TCPKeepAliveIdle > 0 {
			return cfg.Defaults.TCPKeepAliveIdle
		}
		return 30 * time.Second
	case "ka_intvl":
		if bset != nil && bset.bcfg.TCPKeepAliveIntvl > 0 {
			return bset.bcfg.TCPKeepAliveIntvl
		}
		if cfg.Defaults.TCPKeepAliveIntvl > 0 {
			return cfg.Defaults.TCPKeepAliveIntvl
		}
		return 5 * time.Second
	default:
		return 0
	}
}
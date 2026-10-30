package proxy

import (
	"context"
	"errors"
	"lsh-utproxy/internal/acl"
	"lsh-utproxy/internal/balancer"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/stats"
	"lsh-utproxy/internal/util"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// tcpDialRetries is how many backend servers one client connection may be
// pointed at before it is refused (HAProxy "retries").
const tcpDialRetries = 2

// closeBlocked ends a connection refused by an access rule.
//
// "deny" is the iptables REJECT behaviour: SO_LINGER 0 makes Close send a TCP
// reset, so the client's connect() ends with "connection refused" at once
// instead of hanging. "drop" is silent: the socket is closed with a normal FIN
// and the client sees nothing until its own timeout fires.
func closeBlocked(c net.Conn, act acl.Action) {
	if act == acl.ActionDeny {
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
	}
	_ = c.Close()
}

// listenTCP binds the frontend socket. SO_REUSEPORT is used only where the OS
// supports it (Linux); elsewhere a plain bind is used and Run logs a warning.
func listenTCP(cfg *config.Config, addr string) (net.Listener, error) {
	if cfg.Defaults.ReUsePort && util.ReusePortSupported {
		return util.ListenTCPReuse(addr)
	}
	return net.Listen("tcp", addr)
}

// serveTCP accepts connections on an already bound listener until ctx is done.
// aclList is the frontend's ordered allow/deny/drop rules; nil, or an empty
// list, means no filtering.
func serveTCP(ctx context.Context, cfg *config.Config, fe config.Frontend, aclList *acl.List, ln net.Listener, addr string, backendMap map[string]*BackendSet, st *stats.Stats, feLim *connLimiter, globLim *connLimiter) error {
	var wg sync.WaitGroup
	// Unblock Accept() on shutdown; established connections are drained by
	// RunWithCtx within the grace window.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		if aclList.Len() > 0 {
			act, rule := aclList.Decide(acl.ClientIP(conn.RemoteAddr()))
			if act != acl.ActionAllow {
				st.Blocked.Add(1)
				if aclList.ShouldLog() {
					log.Info().Str("frontend", fe.Name).Str("rule", rule).Str("action", act.String()).
						Str("src", conn.RemoteAddr().String()).
						Msg("connection refused by access rule")
				}
				closeBlocked(conn, act)
				continue
			}
		}
		if !feLim.tryAcquire() {
			st.Dropped.Add(1)
			log.Debug().Str("frontend", fe.Name).Msg("frontend maxconn reached, connection refused")
			_ = conn.Close()
			continue
		}
		if !globLim.tryAcquire() {
			feLim.release()
			st.Dropped.Add(1)
			log.Debug().Str("frontend", fe.Name).Msg("global maxconn reached, connection refused")
			_ = conn.Close()
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer feLim.release()
			defer globLim.release()
			defer c.Close()
			handleTCP(ctx, cfg, fe, c, backendMap, st)
		}(conn)
	}
	wg.Wait()
	return ctx.Err()
}

func handleTCP(ctx context.Context, cfg *config.Config, fe config.Frontend, cli net.Conn, backendMap map[string]*BackendSet, st *stats.Stats) {
	st.ConnsTotal.Add(1)
	st.ConnsOpen.Add(1)
	defer st.ConnsOpen.Add(-1)

	bn := fe.DefaultBackend
	bset, ok := backendMap[bn]
	if !ok || bset == nil {
		log.Warn().Str("frontend", fe.Name).Str("backend", bn).Msg("backend not found")
		st.Errors.Add(1)
		return
	}

	// Pick a server and dial it; on connect failure try another server before
	// giving up, instead of dropping the client on a single backend blip.
	bkey := balancer.HostOnly(cli.RemoteAddr().String())
	var beConn net.Conn
	var bsrvS *BackendServer
	var dialErr error
	for attempt := 0; attempt < tcpDialRetries; attempt++ {
		bsrv, berr := bset.Next(bkey)
		if berr != nil {
			dialErr = berr
			if attempt+1 < tcpDialRetries {
				time.Sleep(10 * time.Millisecond)
			}
			continue
		}
		srv, ok2 := bsrv.(*BackendServer)
		if !ok2 {
			dialErr = errors.New("unexpected backend server implementation")
			continue
		}
		if !srv.TryInc(srv.MaxConn()) {
			dialErr = ErrServerFull
			st.Dropped.Add(1)
			continue
		}
		d := net.Dialer{Timeout: getTO(cfg, fe, bset, "connect")}
		c, err := d.DialContext(ctx, "tcp", srv.Addr())
		if err != nil {
			srv.Dec()
			dialErr = err
			log.Debug().Err(err).Str("backend", bn).Str("server", srv.ID()).Str("addr", srv.Addr()).Msg("backend dial failed")
			continue
		}
		beConn = c
		bsrvS = srv
		break
	}
	if beConn == nil {
		if !errors.Is(dialErr, ErrServerFull) {
			st.Errors.Add(1)
		}
		if errors.Is(dialErr, balancer.ErrNoHealthy) {
			// Every server is checked-down or missing: worth being loud, but not
			// once per client connection.
			if bset.shouldLogNoServer() {
				st.Dropped.Add(1)
				log.Warn().Str("backend", bn).Str("frontend", fe.Name).
					Msg("no server available in this backend (all checked down or none configured), connection refused")
			}
		} else {
			log.Debug().Err(dialErr).Str("backend", bn).Str("frontend", fe.Name).Msg("no backend connection")
		}
		return
	}
	defer bsrvS.Dec()
	defer beConn.Close()

	if getBool(cfg, fe, bset, "tcpka") {
		idle := getDur(cfg, fe, bset, "ka_idle")
		intvl := getDur(cfg, fe, bset, "ka_intvl")
		cnt := keepAliveCount(cfg, bset)
		applyKeepAlive(beConn, idle, intvl, cnt)
		applyKeepAlive(cli, idle, intvl, cnt)
	}

	to := copyTimeouts{writeTO: defaultWriteTO}
	to.clientIdle, to.serverIdle = idleTimeouts(cfg, fe, bset)

	nIn, nOut, eIn, eOut := copyBidir(beConn, cli, st, to)
	log.Debug().
		Str("frontend", fe.Name).
		Str("backend", bn).
		Str("server", bsrvS.ID()).
		Int64("bytes_in", nIn).
		Int64("bytes_out", nOut).
		Msg("session closed")

	if isIdleTimeout(eIn) || isIdleTimeout(eOut) {
		st.Timeouts.Add(1)
		log.Info().
			Str("frontend", fe.Name).
			Str("backend", bn).
			Str("server", bsrvS.ID()).
			Int64("bytes_in", nIn).
			Int64("bytes_out", nOut).
			Dur("idle_client", to.clientIdle).
			Dur("idle_server", to.serverIdle).
			Msg("session closed by inactivity timeout")
	} else {
		if eIn != nil && !isCloseErr(eIn) {
			st.Errors.Add(1)
			log.Warn().Err(eIn).Str("frontend", fe.Name).Str("server", bsrvS.ID()).Int64("bytes_in", nIn).Msg("tcp proxy err (client -> backend)")
		}
		if eOut != nil && !isCloseErr(eOut) {
			st.Errors.Add(1)
			log.Warn().Err(eOut).Str("frontend", fe.Name).Str("server", bsrvS.ID()).Int64("bytes_out", nOut).Msg("tcp proxy err (backend -> client)")
		}
	}
}

var keepAliveWarnOnce sync.Once

// keepAliveCount returns the number of failed probes before a connection is
// considered dead (Linux only; the section value wins over defaults).
func keepAliveCount(cfg *config.Config, bset *BackendSet) int {
	if bset != nil && bset.bcfg.TCPKeepAliveCnt > 0 {
		return bset.bcfg.TCPKeepAliveCnt
	}
	if cfg.Defaults.TCPKeepAliveCnt > 0 {
		return cfg.Defaults.TCPKeepAliveCnt
	}
	return 3
}

// applyKeepAlive enables keepalive on both halves of a session. On Linux the
// interval and probe count from the config are applied as well, which is what
// makes a dead peer disappear after idle + interval*count instead of the kernel
// default of two hours.
func applyKeepAlive(c net.Conn, idle, intvl time.Duration, cnt int) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	if err := util.ApplyTCPKeepAlive(tc, idle, intvl, cnt); err != nil {
		log.Debug().Err(err).Msg("setsockopt tcp keepalive failed")
		return
	}
	if (intvl > 0 || cnt > 0) && !util.KeepAliveFullSupported {
		keepAliveWarnOnce.Do(func() {
			log.Warn().Msg("tcp keepalive interval and count are Linux-only; the idle time is what is applied on this OS")
		})
	}
}

// idleTimeouts resolves the per-direction inactivity timers, HAProxy style:
//
//	timeout tunnel  -> applied to both directions (tunneled, idle on either side)
//	timeout idle    -> project-specific fallback, both directions
//	timeout client   -> client direction only
//	timeout server   -> backend direction only
//
// A timeout configured nowhere falls back to one hour, so a long-lived but
// idle RDP/VPN session is not killed by a short default.
func idleTimeouts(cfg *config.Config, fe config.Frontend, bset *BackendSet) (clientIdle, serverIdle time.Duration) {
	var tun, idle, srv time.Duration
	if bset != nil {
		tun = bset.bcfg.TimeoutTunnel
		idle = bset.bcfg.TimeoutIdle
		srv = bset.bcfg.TimeoutServer
	}
	if d := firstPositive(tun, fe.TimeoutTunnel, cfg.Defaults.TimeoutTunnel); d > 0 {
		return d, d
	}
	if d := firstPositive(idle, fe.TimeoutIdle, cfg.Defaults.TimeoutIdle); d > 0 {
		return d, d
	}
	clientIdle = firstPositive(fe.TimeoutClient, cfg.Defaults.TimeoutClient)
	serverIdle = firstPositive(srv, cfg.Defaults.TimeoutServer)
	if clientIdle <= 0 {
		clientIdle = serverIdle
	}
	if serverIdle <= 0 {
		serverIdle = clientIdle
	}
	if clientIdle <= 0 {
		clientIdle = 1 * time.Hour
		serverIdle = 1 * time.Hour
	}
	return clientIdle, serverIdle
}

func firstPositive(vals ...time.Duration) time.Duration {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func getTO(cfg *config.Config, fe config.Frontend, bset *BackendSet, k string) time.Duration {
	var becfg *config.Backend
	if bset != nil {
		becfg = bset.bcfg
	}
	switch k {
	case "connect":
		if becfg != nil && becfg.TimeoutConnect > 0 {
			return becfg.TimeoutConnect
		}
		if cfg.Defaults.TimeoutConnect > 0 {
			return cfg.Defaults.TimeoutConnect
		}
		return 5 * time.Second
	case "idle":
		c, _ := idleTimeouts(cfg, fe, bset)
		return c
	case "server":
		if becfg != nil && becfg.TimeoutServer > 0 {
			return becfg.TimeoutServer
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
		if becfg != nil && becfg.TimeoutTunnel > 0 {
			return becfg.TimeoutTunnel
		}
		if fe.TimeoutTunnel > 0 {
			return fe.TimeoutTunnel
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

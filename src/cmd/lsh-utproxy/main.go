package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"

	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/logging"
	"lsh-utproxy/internal/proxy"
	"lsh-utproxy/internal/service"
	"lsh-utproxy/internal/util"

	"github.com/rs/zerolog/log"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func usage() {
	fmt.Fprintf(os.Stderr, `lsh-utproxy %s (%s, built %s) - TCP/UDP proxy with a HAProxy-like config

usage: lsh-utproxy [-c config] [-t] [-service] [-v]

  -c, --config <file>    config file (default: %s)
  -t, --test             parse the config, print the summary, exit 0/1
  -service               run as a Windows service (installed with sc.exe)
  -service-name <name>   Windows service name (default: lsh-utproxy)
  -v, --version          print the version and exit

Start it from a console to test it, or install it as a Windows service
(README: Windows service) or a systemd unit (deploy/lsh-utproxy.service).
`, version, commit, date, util.DefaultConfigPath())
}

func main() {
	var (
		cfgPath   string
		cfgPath2  string
		showVer   bool
		showVer2  bool
		testOnly  bool
		testOnly2 bool
		svcMode   bool
		svcName   string
	)
	flag.StringVar(&cfgPath, "c", "", "path to the config file")
	flag.StringVar(&cfgPath2, "config", "", "path to the config file (long form)")
	flag.BoolVar(&testOnly, "t", false, "check the config and exit")
	flag.BoolVar(&testOnly2, "test", false, "check the config and exit (long form)")
	flag.BoolVar(&svcMode, "service", false, "run as a Windows service")
	flag.StringVar(&svcName, "service-name", "lsh-utproxy", "Windows service name")
	flag.BoolVar(&showVer, "v", false, "print version and exit")
	flag.BoolVar(&showVer2, "version", false, "print version and exit (long form)")
	flag.Usage = usage
	flag.Parse()

	if showVer || showVer2 {
		fmt.Printf("lsh-utproxy %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	path := util.ResolveConfigPath(firstNonEmpty(cfgPath, cfgPath2))
	cfg, err := loadConfig(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
		os.Exit(1)
	}
	logging.Setup(cfg)

	if testOnly || testOnly2 {
		printSummary(cfg)
		return
	}

	log.Info().
		Str("version", version).
		Str("commit", commit).
		Str("built", date).
		Str("config", path).
		Str("platform", runtime.GOOS+"/"+runtime.GOARCH).
		Msg("lsh-utproxy starting")

	// A process started by the Service Control Manager must talk the service
	// protocol, otherwise the SCM kills it with error 1053. Running from a
	// console uses the signal-driven context instead.
	if svcMode || service.RunningUnderSCM() {
		if err := service.Run(svcName, func(ctx context.Context) error {
			return proxy.RunWithCtx(ctx, cfg)
		}); err != nil {
			log.Error().Err(err).Msg("service run failed")
			fmt.Fprintf(os.Stderr, "service error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := proxy.Run(cfg); err != nil {
		log.Error().Err(err).Msg("proxy exited with an error")
		os.Exit(1)
	}
}

func loadConfig(path string) (*config.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return config.NewParser(f).Config()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// printSummary shows what the config declares. Used by -t, so an operator can
// check a file before restarting the service.
func printSummary(cfg *config.Config) {
	fmt.Printf("config OK: %d frontend(s), %d backend(s)\n", len(cfg.Frontends), len(cfg.Backends))
	for _, fe := range cfg.Frontends {
		mode := firstNonEmpty(fe.Mode, cfg.Defaults.Mode, "tcp")
		fmt.Printf("  frontend %-24s mode=%-4s default_backend=%-24s maxconn=%d\n", fe.Name, mode, fe.DefaultBackend, fe.MaxConn)
		for _, b := range fe.Bind {
			fmt.Printf("      bind %s\n", b.Addr)
		}
		// Access rules in file order: the first rule that matches the source
		// address wins, so printing them in order shows the real behaviour.
		for _, r := range fe.AccessRules {
			fmt.Printf("      %s %s\n", r.Action, r.Addr)
		}
	}
	for _, be := range cfg.Backends {
		mode := firstNonEmpty(be.Mode, cfg.Defaults.Mode, "tcp")
		fmt.Printf("  backend  %-24s mode=%-4s balance=%-11s servers=%d\n", be.Name, mode, firstNonEmpty(be.Balance, cfg.Defaults.Balance, "roundrobin"), len(be.Servers))
		for _, s := range be.Servers {
			flags := ""
			if s.Check {
				flags += " check"
			}
			if s.Backup {
				flags += " backup"
			}
			if s.MaxConn > 0 {
				flags += fmt.Sprintf(" maxconn=%d", s.MaxConn)
			}
			fmt.Printf("      server %-20s %-24s weight=%d%s\n", s.Name, s.Addr, s.Weight, flags)
		}
	}
	if cfg.Defaults.ReUsePort && !util.ReusePortSupported {
		fmt.Println("  note: re-use-port is not supported on this OS, a plain bind is used")
	}
	if cfg.Global.Chroot != "" || cfg.Global.User != "" || cfg.Global.Group != "" {
		if util.PrivilegesSupported {
			fmt.Printf("  note: privileges dropped after bind: chroot=%q user=%q group=%q\n", cfg.Global.Chroot, cfg.Global.User, cfg.Global.Group)
		} else {
			fmt.Println("  note: chroot/user/group are ignored on this OS; set the service account instead")
		}
	}
}

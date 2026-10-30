package main

import (
	"flag"
	"fmt"
	"lsh-utproxy/internal/config"
	"lsh-utproxy/internal/proxy"
	"os"

	"github.com/rs/zerolog/log"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	var cfgPath string
	var showVer bool
	flag.StringVar(&cfgPath, "c", "config/example.cfg", "path to config file (HAProxy-like)")
	flag.StringVar(&cfgPath, "config", "config/example.cfg", "path to config file")
	flag.BoolVar(&showVer, "v", false, "show version")
	flag.BoolVar(&showVer, "version", false, "show version")
	flag.Parse()
	if showVer {
		fmt.Printf("lsh-utproxy %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	f, err := os.Open(cfgPath)
	if err != nil {
		log.Fatal().Err(err).Str("path", cfgPath).Msg("cannot open config")
	}
	defer f.Close()
	p := config.NewParser(f)
	cfg, err := p.Config()
	if err != nil {
		log.Fatal().Err(err).Msg("cannot parse config")
	}
	log.Info().
		Int("frontends", len(cfg.Frontends)).
		Int("backends", len(cfg.Backends)).
		Msg("config parsed")
	for _, fe := range cfg.Frontends {
		log.Info().Str("frontend", fe.Name).Str("mode", fe.Mode).Msg("frontend loaded")
		for _, b := range fe.Bind {
			log.Info().Str("frontend", fe.Name).Str("addr", b.Addr).Int("port", b.Port).Msg("bind")
		}
	}
	if err := proxy.Run(cfg); err != nil {
		log.Fatal().Err(err).Msg("proxy exited")
	}
}
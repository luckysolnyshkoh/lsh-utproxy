// Package logging configures the process logger for console and service runs.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lsh-utproxy/internal/config"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Setup points the global zerolog logger at the target configured in the
// "global" section: stdout, stderr or a file path (append mode).
//
// A file target is what a Windows service or a systemd unit without a journal
// capture needs; if the file cannot be opened the logger falls back to stderr
// and says so.
//
// Calling Setup twice (once from main, once when the proxy starts) closes the
// previously opened log file, so no handle is leaked.
func Setup(cfg *config.Config) {
	if currentFile != nil {
		_ = currentFile.Close()
		currentFile = nil
	}
	zerolog.SetGlobalLevel(levelFor(cfg.Global.LogLevel))

	target := strings.TrimSpace(cfg.Global.LogTarget)
	jsonFormat := strings.EqualFold(cfg.Global.LogFormat, "json")

	switch strings.ToLower(target) {
	case "", "stdout", "-":
		apply(os.Stdout, jsonFormat, false)
	case "stderr":
		apply(os.Stderr, jsonFormat, false)
	default:
		// Create the log directory if it is missing: a fresh install with
		// "log <install>/logs/app.log" should not need a manual mkdir.
		if dir := filepath.Dir(target); dir != "" && dir != "." && dir != string(filepath.Separator) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "cannot create log directory %q: %v, logging to stderr\n", dir, err)
				apply(os.Stderr, jsonFormat, false)
				return
			}
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot open log file %q: %v, logging to stderr\n", target, err)
			apply(os.Stderr, jsonFormat, false)
			return
		}
		currentFile = f
		// No ANSI colours in a file.
		apply(f, jsonFormat, true)
	}
}

// currentFile is the log file opened by Setup, if any.
var currentFile *os.File

func apply(w io.Writer, jsonFormat, noColor bool) {
	if jsonFormat {
		log.Logger = zerolog.New(w).With().Timestamp().Logger()
		return
	}
	cw := zerolog.NewConsoleWriter(func(c *zerolog.ConsoleWriter) {
		c.TimeFormat = "2006-01-02T15:04:05Z07:00"
		c.NoColor = noColor
	})
	log.Logger = zerolog.New(cw).With().Timestamp().Logger()
}

func levelFor(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "none", "off":
		return zerolog.Disabled
	default:
		return zerolog.InfoLevel
	}
}

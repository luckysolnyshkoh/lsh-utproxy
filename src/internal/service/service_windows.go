//go:build windows

package service

import (
	"context"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"

	"github.com/rs/zerolog/log"
)

// EventLogSource is the Windows Event Log source the service writes to. The
// install script registers it with New-EventLog; when the source is missing the
// service still runs and only the file/stdout log gets the messages.
const EventLogSource = "lsh-utproxy"

// RunningUnderSCM reports whether the process was started by the Service
// Control Manager, so the binary can pick service mode even without "-service".
func RunningUnderSCM() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// Run executes start under the Service Control Manager. The context passed to
// start is canceled when SCM sends STOP or SHUTDOWN; Run returns after start
// returns, and the SCM sees the service as stopped.
func Run(name string, start func(context.Context) error) error {
	return svc.Run(name, &handler{start: start})
}

type handler struct {
	start func(context.Context) error
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	elog, err := eventlog.Open(EventLogSource)
	if err != nil {
		elog = nil
	}
	notify := func(level int32, msg string) {
		if elog == nil {
			return
		}
		switch level {
		case 1:
			_ = elog.Info(1, msg)
		default:
			_ = elog.Error(2, msg)
		}
	}

	s <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- h.start(ctx) }()

	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	notify(1, "lsh-utproxy started")
	log.Info().Msg("running as a Windows service")

	for {
		select {
		case err := <-done:
			s <- svc.Status{State: svc.StopPending}
			if err != nil {
				notify(2, "lsh-utproxy stopped with an error: "+err.Error())
				log.Error().Err(err).Msg("service stopped with an error")
				return false, 1
			}
			notify(1, "lsh-utproxy stopped")
			log.Info().Msg("service stopped")
			return false, 0
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				log.Info().Int32("control", int32(c.Cmd)).Msg("Service Control Manager asked to stop")
				s <- svc.Status{State: svc.StopPending}
				cancel()
			case svc.Interrogate:
				s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			default:
				log.Debug().Int32("control", int32(c.Cmd)).Msg("ignoring service control request")
			}
		}
	}
}

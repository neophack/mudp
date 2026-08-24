// mudp entry point. The binary is both a console program (plain `mudp` runs
// the server in the foreground) and an OS service binary: `mudp install`
// registers it with the Windows service controller or systemd, after which
// the supervisor owns restarts — including the restart after a self-upgrade
// (the upgrade path swaps the files and exits; it never spawns children under
// a supervisor, so a closing console or session cannot take the replacement
// down with it).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
	// Embed the IANA timezone database: Windows hosts have no system zoneinfo,
	// so without this time.LoadLocation fails for every named timezone and the
	// GeoIP/browser timezone comparison in security.go always reports a
	// mismatch.
	_ "time/tzdata"

	"github.com/kardianos/service"

	"mudp/internal/config"
	"mudp/internal/server"
	"mudp/internal/store"
	"mudp/internal/upgrader"
	"mudp/internal/version"
)

// controlActions are the service-management subcommands handled via
// service.Control (which talks to the Windows SCM / systemctl).
var controlActions = map[string]bool{
	"install":   true,
	"uninstall": true,
	"start":     true,
	"stop":      true,
	"restart":   true,
}

func main() {
	showVersion := flag.Bool("version", false, "print the build version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.Version)
		return
	}

	if args := flag.Args(); len(args) > 0 {
		if args[0] == "status" {
			serviceStatus()
			return
		}
		if controlActions[args[0]] {
			control(args[0])
			return
		}
	}

	p := &program{stopCh: make(chan struct{}), done: make(chan struct{})}
	s, err := service.New(p, svcConfig())
	if err != nil {
		log.Fatalf("create service: %v", err)
	}
	if err := s.Run(); err != nil {
		log.Fatalf("run mudp: %v", err)
	}
}

// svcConfig describes the mudp service for install/uninstall. On Windows the
// recovery actions restart the service 5s after any failure (a non-zero exit
// is how the upgrade path signals "restart me"); on Linux the systemd
// template already defaults to Restart=always.
func svcConfig() *service.Config {
	c := &service.Config{
		Name:        "mudp",
		DisplayName: "mudp",
		Description: "mudp container console",
	}
	if runtime.GOOS == "windows" {
		c.Option = service.KeyValue{
			"StartType":              "automatic",
			"OnFailure":              "restart",
			"OnFailureDelayDuration": "5s",
			"OnFailureResetPeriod":   "86400",
		}
	}
	return c
}

// control runs a service-management subcommand (requires administrator on
// Windows, root on Linux).
func control(action string) {
	s, err := service.New(&program{stopCh: make(chan struct{}), done: make(chan struct{})}, svcConfig())
	if err != nil {
		log.Fatalf("create service: %v", err)
	}
	if err := service.Control(s, action); err != nil {
		fmt.Fprintf(os.Stderr, "mudp service %s failed (administrator/root required?): %v\n", action, err)
		os.Exit(1)
	}
	if action == "install" && runtime.GOOS == "linux" {
		tuneSystemdUnit()
	}
	fmt.Printf("mudp service: %s ok\n", action)
}

// serviceStatus prints whether the mudp service is installed/running.
func serviceStatus() {
	s, err := service.New(&program{stopCh: make(chan struct{}), done: make(chan struct{})}, svcConfig())
	if err != nil {
		log.Fatalf("create service: %v", err)
	}
	status, err := s.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mudp service status failed: %v\n", err)
		os.Exit(1)
	}
	switch status {
	case service.StatusRunning:
		fmt.Println("mudp service: running")
	case service.StatusStopped:
		fmt.Println("mudp service: stopped")
	default:
		fmt.Println("mudp service: unknown")
	}
}

// tuneSystemdUnit rewrites the RestartSec kardianos hardcodes (120s) down to
// 5s so an upgrade restart comes back in seconds, then reloads systemd.
func tuneSystemdUnit() {
	const unitPath = "/etc/systemd/system/mudp.service"
	data, err := os.ReadFile(unitPath)
	if err != nil {
		return
	}
	if !strings.Contains(string(data), "RestartSec=120") {
		return
	}
	if err := os.WriteFile(unitPath, []byte(strings.Replace(string(data), "RestartSec=120", "RestartSec=5", 1)), 0o644); err != nil {
		log.Printf("tune systemd unit: %v", err)
		return
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		log.Printf("systemctl daemon-reload: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// program adapts the server to the service framework: Start launches the
// server goroutine, Stop asks it to shut down and waits. In interactive
// (console) mode the framework calls the same pair, so both modes share one
// lifecycle.
type program struct {
	stopCh chan struct{}
	done   chan struct{}
}

func (p *program) Start(s service.Service) error {
	// Route the standard logger into the service log too (Windows event log /
	// systemd journal): when running headless as a service this is the only
	// place startup failures and upgrade progress land.
	logErrs := make(chan error, 8)
	if lg, _ := s.Logger(logErrs); lg != nil {
		log.SetOutput(io.MultiWriter(os.Stderr, serviceLogWriter{lg}))
	}
	go func() {
		for range logErrs {
		}
	}()
	go p.run()
	return nil
}

func (p *program) Stop(s service.Service) error {
	close(p.stopCh)
	<-p.done
	return nil
}

// serviceLogWriter adapts service.Logger to io.Writer for log.SetOutput.
type serviceLogWriter struct{ lg service.Logger }

func (w serviceLogWriter) Write(b []byte) (int, error) {
	w.lg.Info(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// run is the server body: open the store, build the app, serve, and shut
// down cleanly on p.stopCh, a signal, or a fatal serve error.
func (p *program) run() {
	defer close(p.done)

	cfg := config.Load()

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if err := db.Migrate(cfg.AdminUser, cfg.AdminPassword); err != nil {
		// A failed migration on a just-swapped binary means the upgrade cannot
		// serve: roll the previous version back before dying so a supervisor
		// (or the old watcher process) restarts the working build.
		if exe, xerr := os.Executable(); xerr == nil && upgrader.ReadMarker(exe) != nil {
			if rerr := upgrader.Rollback(exe); rerr != nil {
				log.Printf("upgrade rollback after migration failure: %v", rerr)
			} else {
				log.Printf("migration failed: %v — rolled back to previous binary", err)
			}
		}
		log.Fatalf("migrate database: %v", err)
	}

	app, err := server.New(cfg, db)
	if err != nil {
		log.Fatalf("start mudp: %v", err)
	}

	// Bring up the external MCP listener if an admin has configured one. A
	// failure here (usually a port already in use) must not stop the console
	// from starting: the admin can fix the port from Settings once they are in.
	if err := app.ApplyRemoteMCP(); err != nil {
		log.Printf("WARNING: external MCP access not started: %v", err)
	}

	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	stopJobs := app.StartBackgroundJobs(backgroundCtx)
	defer stopJobs()
	defer cancelBackground()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout is intentionally disabled too: it bounds the ENTIRE request
		// read in net/http, body included, not just headers. A netdisk/volume
		// chunk upload (up to ~100+ MiB) over a slow link can legitimately take
		// longer than any fixed bound to arrive; a ReadTimeout here would sever
		// the connection mid-upload and turn a slow network into a hard failure.
		// ReadHeaderTimeout above still bounds the slow-header-only case, and
		// body size is capped independently via http.MaxBytesReader per handler.
		ReadTimeout: 0,
		// WriteTimeout is intentionally disabled: long-running Server-Sent Events
		// streams (container creation, fused image builds) can run for many
		// minutes. Each handler uses its own context deadline instead.
		WriteTimeout:   0,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MiB
	}

	// The upgrade flow (server/upgrade.go) needs to release this listener
	// before the replacement process can bind it; hand it a graceful-shutdown
	// hook wired to srv.
	app.SetRestartPrepare(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("upgrade shutdown: %v", err)
		}
	})

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("mudp %s listening on http://%s", version.Version, cfg.Addr)
		serveErr <- srv.ListenAndServe()
	}()

	// Upgrade verification: a marker left by the previous process means this
	// boot is an upgrade attempt. Serve first; once healthy for a while,
	// commit (drop backup + marker). If we never become healthy, roll back to
	// the previous binary and exit non-zero so any supervisor restarts it.
	if exe, err := os.Executable(); err == nil && upgrader.ReadMarker(exe) != nil {
		go verifyUpgrade(exe, cfg.Addr)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-p.stopCh:
	case <-stop:
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			rollbackFatal(exePath(), err)
			log.Fatalf("http server: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown error: %v", err)
	}
	if err := app.Close(); err != nil {
		log.Printf("close app: %v", err)
	}
}

// verifyUpgrade watches a freshly-swapped process: healthy for the stability
// window commits the upgrade; never-healthy rolls it back and exits so the
// supervisor (or the old watcher process) brings the previous version back.
func verifyUpgrade(exe, addr string) {
	marker := upgrader.ReadMarker(exe)
	if marker == nil {
		return
	}
	if err := upgrader.WaitHealthy(addr, 30*time.Second); err != nil {
		rollbackFatal(exe, err)
		os.Exit(1)
	}
	time.Sleep(20 * time.Second)
	if err := upgrader.Commit(exe); err != nil {
		log.Printf("upgrade commit: %v", err)
		return
	}
	log.Printf("upgrade to %s verified and committed", marker.To)
}

func exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return exe
}

// rollbackFatal rolls the previous binary back (when this boot was an upgrade
// attempt) so the next start — by the service manager, the old watcher
// process, or the operator — runs the working version again.
func rollbackFatal(exe string, cause error) {
	if exe == "" || upgrader.ReadMarker(exe) == nil {
		return
	}
	if err := upgrader.Rollback(exe); err != nil {
		log.Printf("rollback after fatal startup error (%v): %v", cause, err)
		return
	}
	log.Printf("startup failed (%v) — rolled back to previous binary", cause)
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/dockfn/dockfn/internal/app"
	"github.com/dockfn/dockfn/internal/auth"
	"github.com/dockfn/dockfn/internal/config"
	"github.com/dockfn/dockfn/internal/diagnostics"
	"github.com/dockfn/dockfn/internal/fnos"
	apihttp "github.com/dockfn/dockfn/internal/http"
	shellpkg "github.com/dockfn/dockfn/internal/package"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	if code := run(os.Args); code != 0 {
		os.Exit(code)
	}
}

func run(arguments []string) int {
	if len(arguments) < 2 {
		usage()
		return 1
	}
	switch arguments[1] {
	case "server":
		return runServer()
	case "helper":
		return runHelper()
	case "doctor":
		return runDoctor()
	case "prepare-uninstall":
		return runPrepareUninstall(arguments[2:])
	case "version":
		fmt.Printf("DockFN %s commit=%s built=%s go=%s\n", version, commit, buildTime, runtime.Version())
		return 0
	default:
		usage()
		return 1
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: dockfn server|helper|doctor|prepare-uninstall|version")
}

func runServer() int {
	runtimeConfig, err := config.Load()
	if err != nil {
		return fail(err)
	}
	for _, directory := range []string{
		runtimeConfig.DataDir, runtimeConfig.StagingDir,
		filepath.Dir(runtimeConfig.GatewaySocket), filepath.Dir(runtimeConfig.HelperSocket),
	} {
		if err = os.MkdirAll(directory, 0o700); err != nil {
			return fail(err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	repository, err := config.OpenFileStore(runtimeConfig.DataDir)
	if err != nil {
		return fail(err)
	}
	platform := &fnos.Client{
		Socket: runtimeConfig.HelperSocket, StagingDir: runtimeConfig.StagingDir,
		DataDir: runtimeConfig.DataDir, Timeout: runtimeConfig.CommandTimeout + 5*time.Second,
	}
	service := &app.Service{
		Repo: repository,
		Builder: &shellpkg.Builder{
			DataDir: runtimeConfig.DataDir, StagingDir: runtimeConfig.StagingDir,
		},
		Platform: platform, Discoverer: platform, DataDir: runtimeConfig.DataDir, StagingDir: runtimeConfig.StagingDir,
	}
	server := &apihttp.Server{
		Apps: service, Version: version, HelperAvailable: platform.Available,
		Diagnostics:      diagnostics.Reader{LogDir: runtimeConfig.LogDir, DataDir: runtimeConfig.DataDir}.Snapshot,
		ClearDiagnostics: platform.ClearDiagnostics,
	}
	handler := server.Handler()
	var listener net.Listener
	if runtimeConfig.DevListen != "" {
		if !isLoopbackListen(runtimeConfig.DevListen) || os.Getenv("DOCKFN_DEV_ADMIN") != "1" {
			return fail(errors.New("development TCP mode requires a loopback address and DOCKFN_DEV_ADMIN=1"))
		}
		listener, err = net.Listen("tcp", runtimeConfig.DevListen)
		handler = apihttp.AdminHandler(handler, "local-development")
	} else {
		_ = os.Remove(runtimeConfig.GatewaySocket)
		listener, err = net.Listen("unix", runtimeConfig.GatewaySocket)
		if err == nil {
			err = os.Chmod(runtimeConfig.GatewaySocket, 0o660)
		}
		handler = fnOSGateway(handler, runtimeConfig.GatewayPrefix)
	}
	if err != nil {
		return fail(err)
	}
	defer func() {
		_ = listener.Close()
		if runtimeConfig.DevListen == "" {
			_ = os.Remove(runtimeConfig.GatewaySocket)
		}
	}()
	httpServer := &http.Server{
		Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: runtimeConfig.CommandTimeout + 10*time.Second, IdleTimeout: 2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	slog.Info("DockFN server started", "address", listener.Addr().String(), "version", version)
	err = httpServer.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fail(err)
	}
	return 0
}

func runHelper() int {
	runtimeConfig, err := config.Load()
	if err != nil {
		return fail(err)
	}
	current, err := user.Current()
	if err != nil || (runtime.GOOS != "windows" && current.Uid != "0") {
		return fail(errors.New("DockFN helper must run as root"))
	}
	gid, err := fnos.ParseSocketGID(os.Getenv("DOCKFN_HELPER_GID"))
	if err != nil {
		return fail(err)
	}
	for _, directory := range []string{runtimeConfig.DataDir, runtimeConfig.StagingDir, filepath.Dir(runtimeConfig.HelperSocket)} {
		if err = os.MkdirAll(directory, 0o750); err != nil {
			return fail(err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	helper := &fnos.Helper{
		Socket: runtimeConfig.HelperSocket, StagingDir: runtimeConfig.StagingDir,
		DataDir: runtimeConfig.DataDir, LogDir: runtimeConfig.LogDir, AppCenterCLI: runtimeConfig.AppCenterCLI,
		Fnpack: runtimeConfig.Fnpack, DockerCLI: runtimeConfig.DockerCLI, InstallVolume: runtimeConfig.InstallVolume,
		SocketGID: gid, CommandTimeout: runtimeConfig.CommandTimeout,
	}
	slog.Info("DockFN privilege helper started", "socket", runtimeConfig.HelperSocket)
	if err = helper.Listen(ctx); err != nil {
		return fail(err)
	}
	return 0
}

func runDoctor() int {
	runtimeConfig, err := config.Load()
	if err != nil {
		return fail(err)
	}
	checks := map[string]any{
		"product": "DockFN", "version": version, "architecture": runtime.GOARCH,
		"dataDirectoryAbsolute":    filepath.IsAbs(runtimeConfig.DataDir),
		"stagingDirectoryAbsolute": filepath.IsAbs(runtimeConfig.StagingDir),
		"appCenterCLI":             regularExecutable(runtimeConfig.AppCenterCLI),
		"fnpack":                   regularExecutable(runtimeConfig.Fnpack),
		"helperSocket":             socketExists(runtimeConfig.HelperSocket),
		"gatewaySocket":            socketExists(runtimeConfig.GatewaySocket),
	}
	_ = json.NewEncoder(os.Stdout).Encode(checks)
	if !checks["dataDirectoryAbsolute"].(bool) || !checks["stagingDirectoryAbsolute"].(bool) {
		return 2
	}
	return 0
}

func runPrepareUninstall(arguments []string) int {
	flags := flag.NewFlagSet("prepare-uninstall", flag.ContinueOnError)
	registrations := flags.String("registrations", "keep", "keep or remove")
	if err := flags.Parse(arguments); err != nil {
		return 1
	}
	if *registrations != "keep" && *registrations != "remove" {
		return fail(errors.New("registrations must be keep or remove"))
	}
	if *registrations == "keep" {
		fmt.Println("DockFN registrations preserved; target services and data were not touched")
		return 0
	}
	runtimeConfig, err := config.Load()
	if err != nil {
		return fail(err)
	}
	repository, err := config.OpenFileStore(runtimeConfig.DataDir)
	if err != nil {
		return fail(err)
	}
	platform := &fnos.Client{
		Socket: runtimeConfig.HelperSocket, StagingDir: runtimeConfig.StagingDir,
		DataDir: runtimeConfig.DataDir, Timeout: runtimeConfig.CommandTimeout + 5*time.Second,
	}
	service := &app.Service{Repo: repository, Platform: platform, DataDir: runtimeConfig.DataDir, StagingDir: runtimeConfig.StagingDir}
	specs, err := repository.List(context.Background())
	if err != nil {
		return fail(err)
	}
	for _, spec := range specs {
		if err = service.Remove(context.Background(), spec.ID); err != nil {
			return fail(fmt.Errorf("remove %s: %w", spec.AppName, err))
		}
	}
	fmt.Printf("removed %d DockFN-owned registrations; target services, containers, volumes, and data were not touched\n", len(specs))
	return 0
}

func fnOSGateway(next http.Handler, prefix string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == prefix {
			location := prefix + "/"
			if request.URL.RawQuery != "" {
				location += "?" + request.URL.RawQuery
			}
			http.Redirect(writer, request, location, http.StatusPermanentRedirect)
			return
		}
		if !strings.HasPrefix(request.URL.Path, prefix+"/") {
			http.NotFound(writer, request)
			return
		}
		actorID := strings.TrimSpace(request.Header.Get("X-Trim-Username"))
		if actorID == "" {
			actorID = strings.TrimSpace(request.Header.Get("X-Trim-Userid"))
		}
		admin := strings.EqualFold(strings.TrimSpace(request.Header.Get("X-Trim-Isadmin")), "true")
		clone := request.Clone(request.Context())
		clone.URL.Path = strings.TrimPrefix(request.URL.Path, prefix)
		clone.URL.RawPath = ""
		for _, header := range []string{
			"X-Trim-Username", "X-Trim-Userid", "X-Trim-Isadmin",
			"Authorization", "Cookie", "X-Forwarded-User",
		} {
			clone.Header.Del(header)
		}
		if actorID == "" {
			next.ServeHTTP(writer, clone)
			return
		}
		next.ServeHTTP(writer, auth.WithActor(clone, auth.Actor{ID: actorID, Admin: admin}))
	})
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func regularExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0)
}

func socketExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func fail(err error) int {
	message := err.Error()
	for _, key := range []string{"password", "token", "cookie", "authorization"} {
		if strings.Contains(strings.ToLower(message), key) {
			message = "operation failed; sensitive details were redacted"
			break
		}
	}
	fmt.Fprintln(os.Stderr, message)
	return 1
}

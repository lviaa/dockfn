package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Runtime struct {
	DataDir        string
	LogDir         string
	StagingDir     string
	HelperSocket   string
	GatewaySocket  string
	GatewayPrefix  string
	AppCenterCLI   string
	Fnpack         string
	DockerCLI      string
	InstallVolume  string
	DevListen      string
	CommandTimeout time.Duration
}

func Load() (Runtime, error) {
	dataDir := env("DOCKFN_DATA_DIR", "/var/lib/dockfn")
	config := Runtime{
		DataDir:        dataDir,
		LogDir:         env("DOCKFN_LOG_DIR", filepath.Join(filepath.Dir(dataDir), "logs")),
		StagingDir:     env("DOCKFN_STAGING_DIR", filepath.Join(dataDir, "staging")),
		HelperSocket:   env("DOCKFN_HELPER_SOCKET", filepath.Join(dataDir, "run", "helper.sock")),
		GatewaySocket:  env("DOCKFN_GATEWAY_SOCKET", filepath.Join(dataDir, "run", "app.sock")),
		GatewayPrefix:  env("DOCKFN_GATEWAY_PREFIX", "/app/dockfn"),
		AppCenterCLI:   env("DOCKFN_APPCENTER_CLI", "/usr/local/bin/appcenter-cli"),
		Fnpack:         env("DOCKFN_FNPACK", "/usr/local/bin/fnpack"),
		DockerCLI:      env("DOCKFN_DOCKER_CLI", "/usr/bin/docker"),
		InstallVolume:  env("DOCKFN_INSTALL_VOLUME", "auto"),
		DevListen:      strings.TrimSpace(os.Getenv("DOCKFN_DEV_LISTEN")),
		CommandTimeout: 45 * time.Second,
	}
	if raw := strings.TrimSpace(os.Getenv("DOCKFN_COMMAND_TIMEOUT_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 5 || seconds > 300 {
			return Runtime{}, errors.New("DOCKFN_COMMAND_TIMEOUT_SECONDS must be between 5 and 300")
		}
		config.CommandTimeout = time.Duration(seconds) * time.Second
	}
	for _, path := range []string{config.DataDir, config.LogDir, config.StagingDir, config.HelperSocket, config.GatewaySocket} {
		if !filepath.IsAbs(path) {
			return Runtime{}, errors.New("DockFN data and socket paths must be absolute")
		}
	}
	prefix := strings.TrimSuffix(config.GatewayPrefix, "/")
	if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "..") {
		return Runtime{}, errors.New("DOCKFN_GATEWAY_PREFIX must be an absolute safe path")
	}
	config.GatewayPrefix = prefix
	return config, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

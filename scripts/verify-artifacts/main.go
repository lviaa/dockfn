package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"debug/elf"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

type archiveMember struct {
	body []byte
	mode int64
}

func main() {
	versionFlag := flag.String("version", "", "release version; defaults to VERSION")
	flag.Parse()
	version := strings.TrimSpace(*versionFlag)
	if version == "" {
		body, err := os.ReadFile("VERSION")
		if err != nil {
			panic(fmt.Errorf("read VERSION: %w", err))
		}
		version = strings.TrimSpace(string(body))
		if version == "" {
			panic("VERSION is empty")
		}
	}
	expected := []string{
		"dist/fpk/dockfn-native-arm64.fpk",
		"dist/fpk/dockfn-native-x86_64.fpk",
	}
	actual, err := filepath.Glob("dist/fpk/*.fpk")
	if err != nil {
		panic(err)
	}
	sort.Strings(actual)
	if len(actual) != len(expected) {
		panic(fmt.Sprintf("found %d FPK artifacts, want exactly 2", len(actual)))
	}
	for index, path := range actual {
		if filepath.ToSlash(path) != expected[index] {
			panic(fmt.Sprintf("unexpected FPK artifact %s", path))
		}
		inspectFPK(path, version)
	}
	for _, required := range []string{
		"VERSION", "README.md", "docs/architecture.md", "docs/operations.md", "docs/security.md",
		"docs/release.md", "docs/adr/README.md", "api/openapi.yaml", "dist/SHA256SUMS",
	} {
		requireRegular(required)
	}
	scanSource()
	checkWebBundleSize()
	fmt.Println("artifact-check: 2 native FPKs, both ELF architectures, lifecycle hooks, compact embedded UI, checksums, ownership and source boundaries verified")
}

func checkWebBundleSize() {
	assets, err := filepath.Glob("internal/webui/dist/assets/*.js")
	if err != nil || len(assets) == 0 {
		panic("embedded Web UI JavaScript bundle is missing")
	}
	for _, asset := range assets {
		info, statErr := os.Stat(asset)
		if statErr != nil {
			panic(statErr)
		}
		if info.Size() > 600<<10 {
			panic(fmt.Sprintf("embedded Web UI bundle %s is %d bytes; limit is 614400", asset, info.Size()))
		}
	}
}

func inspectFPK(path, version string) {
	members := readArchive(path, mustOpen(path))
	requiredOuter := []string{
		"app.tgz", "manifest", "ICON.PNG", "ICON_256.PNG", "LICENSE",
		"config/resource", "config/privilege", "wizard/uninstall",
		"cmd/main", "cmd/migrate", "cmd/install_init", "cmd/install_callback",
		"cmd/upgrade_init", "cmd/upgrade_callback", "cmd/uninstall_init",
		"cmd/uninstall_callback", "cmd/config_init", "cmd/config_callback", "cmd/preflight",
	}
	for _, name := range requiredOuter {
		requireMember(path, members, name)
	}
	for _, name := range members {
		_ = name
	}
	for name := range members {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "docker") || strings.Contains(lower, "compose") || strings.Contains(lower, ".env") {
			panic(path + " contains forbidden deployment member " + name)
		}
	}
	manifest := string(members["manifest"].body)
	requireText(path+" manifest", manifest,
		"appname=dockfn", "display_name=飞牛应用坞", "version="+version,
		"desktop_applaunchname=dockfn.main", "maintainer=DockFN Project",
	)
	if strings.Contains(manifest, "wwfn") {
		panic(path + " manifest still exposes the legacy product name")
	}
	appMembers := readArchive(path+" app.tgz", bytes.NewReader(members["app.tgz"].body))
	for _, name := range []string{
		"target/dockfn", "ui/config", "ui/images/icon_64.png", "ui/images/icon_256.png",
		"config/resource", "config/privilege",
	} {
		requireMember(path+" app.tgz", appMembers, name)
	}
	uiConfig := string(appMembers["ui/config"].body)
	requireText(path+" UI config", uiConfig,
		`"dockfn.main"`, `"gatewayPrefix": "/app/dockfn"`, `"gatewaySocket": "app.sock"`,
		`"url": "/app/dockfn/"`, `"allUsers": false`,
	)
	machine := elf.EM_X86_64
	platform, architecture := "platform=x86", "arch=x86_64"
	if strings.HasSuffix(path, "-arm64.fpk") {
		machine = elf.EM_AARCH64
		platform, architecture = "platform=arm", "arch=aarch64"
	}
	requireText(path+" manifest", manifest, platform, architecture)
	binary, err := elf.NewFile(bytes.NewReader(appMembers["target/dockfn"].body))
	if err != nil {
		panic(fmt.Errorf("%s target/dockfn is not ELF: %w", path, err))
	}
	if binary.Machine != machine {
		panic(fmt.Sprintf("%s machine=%s, want %s", path, binary.Machine, machine))
	}
	_ = binary.Close()
	for _, name := range append([]string{"target/dockfn"}, requiredExecutables()...) {
		member := appMembers[name]
		if strings.HasPrefix(name, "cmd/") {
			member = members[name]
		}
		if member.mode&0o111 == 0 {
			panic(path + " member is not executable: " + name)
		}
	}
	checkPNG(path, "ICON.PNG", members["ICON.PNG"].body, 64)
	checkPNG(path, "ICON_256.PNG", members["ICON_256.PNG"].body, 256)
	lifecycle := string(members["cmd/main"].body)
	requireText(path+" lifecycle", lifecycle,
		"DOCKFN_HELPER_SOCKET", "DOCKFN_HELPER_GID", "-- helper", "-- server",
		`gateway_socket="${TRIM_APPDEST}/app.sock"`, "start-stop-daemon",
	)
	if strings.Contains(lifecycle, "bridge") || strings.Contains(lifecycle, "docker") {
		panic(path + " lifecycle retains a removed bridge or Docker role")
	}
	privilege := string(members["config/privilege"].body)
	requireText(path+" privilege", privilege, `"run-as":"root"`, `"username":"dockfn"`, `"groupname":"dockfn"`)
	wizard := string(members["wizard/uninstall"].body)
	requireText(path+" uninstall wizard", wizard, `"field": "keep_registrations"`, `"initValue": "true"`, "目标服务")
}

func scanSource() {
	for _, root := range []string{"cmd", "internal", "web/src"} {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			text := string(body)
			for _, forbidden := range []string{"StatusNotImplemented", " 501 ", "TODO:", "panic(\"TODO\")", "wwfn"} {
				if strings.Contains(text, forbidden) {
					return fmt.Errorf("forbidden placeholder %q in %s", forbidden, path)
				}
			}
			return nil
		})
		if err != nil {
			panic(err)
		}
	}
	for _, removed := range []string{
		"internal/catalog", "internal/reconcile", "internal/exposure", "internal/hostcontrol",
		"internal/appmanager", "deploy/compose", "packaging/fnos/docker",
	} {
		if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
			panic("removed platform module is still present: " + removed)
		}
	}
	server := mustRead("internal/http/server.go")
	for _, forbidden := range []string{"/jobs", "/candidates", "/audit-events", "/settings", "/label-templates", "/auth/"} {
		if strings.Contains(server, forbidden) {
			panic("removed API remains in server: " + forbidden)
		}
	}
	openAPI := mustRead("api/openapi.yaml")
	requireText("OpenAPI", openAPI,
		"openapi: 3.0.3", "/apps:", "/discovery/scan:", "/icons/preview:", "/system/diagnostics:",
		"operationId: clearDiagnostics", "ownerConfidence:", "preferred:", "reports:", "maxLength: 27",
	)
	helper := mustRead("internal/fnos/helper.go")
	requireText("helper diagnostics", helper, `"appcenter-install"`, `"desktop-validation"`, `"DELETE /v1/diagnostics"`)
	cleaner := mustRead("internal/diagnostics/cleaner.go")
	requireText("diagnostic clearing", cleaner,
		`"lifecycle.log", "server.log", "helper.log"`,
		`"last-install-failure.json", "last-discovery.json"`,
	)
	webUI := mustRead("web/src/App.vue")
	requireText("diagnostic clearing UI", webUI, `aria-label="清空诊断历史"`, "确认清空记录")
}

func requiredExecutables() []string {
	return []string{
		"cmd/main", "cmd/migrate", "cmd/install_init", "cmd/install_callback",
		"cmd/upgrade_init", "cmd/upgrade_callback", "cmd/uninstall_init",
		"cmd/uninstall_callback", "cmd/config_init", "cmd/config_callback", "cmd/preflight",
	}
}

func mustOpen(path string) io.Reader {
	file, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	return file
}

func readArchive(subject string, input io.Reader) map[string]archiveMember {
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		panic(fmt.Errorf("%s: %w", subject, err))
	}
	defer func() { _ = gzipReader.Close() }()
	reader := tar.NewReader(gzipReader)
	members := map[string]archiveMember{}
	for {
		header, nextErr := reader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			panic(nextErr)
		}
		name := strings.TrimPrefix(header.Name, "./")
		if header.Typeflag == tar.TypeDir {
			continue
		}
		clean := pathpkg.Clean(name)
		if clean != name || strings.HasPrefix(name, "/") || strings.HasPrefix(clean, "../") || strings.Contains(name, `\`) {
			panic(subject + " contains unsafe archive path " + name)
		}
		if _, exists := members[name]; exists {
			panic(subject + " contains duplicate archive member " + name)
		}
		if header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink {
			panic(subject + " contains an archive link " + name)
		}
		body, readErr := io.ReadAll(io.LimitReader(reader, 64<<20+1))
		if readErr != nil || len(body) > 64<<20 {
			panic(subject + " contains an invalid or oversized member " + name)
		}
		members[name] = archiveMember{body: body, mode: header.Mode}
	}
	return members
}

func requireRegular(path string) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		panic("missing or empty required file " + path)
	}
}

func requireMember(subject string, members map[string]archiveMember, name string) {
	member, exists := members[name]
	if !exists || len(member.body) == 0 {
		panic(subject + " missing or empty " + name)
	}
}

func requireText(subject, body string, values ...string) {
	for _, value := range values {
		if !strings.Contains(body, value) {
			panic(subject + " missing " + value)
		}
	}
}

func checkPNG(subject, name string, body []byte, size int) {
	config, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil || config.Width != size || config.Height != size {
		panic(fmt.Sprintf("%s %s must be a %dx%d PNG", subject, name, size, size))
	}
}

func mustRead(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(body)
}

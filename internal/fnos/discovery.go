package fnos

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dockfn/dockfn/internal/app"
	shellpkg "github.com/dockfn/dockfn/internal/package"
)

var (
	listenerProcess  = regexp.MustCompile(`\("([^" ]+)"`)
	listenerPID      = regexp.MustCompile(`pid=([0-9]+)`)
	titlePattern     = regexp.MustCompile(`(?is)<title[^>]*>\s*(.*?)\s*</title>`)
	linkPattern      = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	attributePattern = regexp.MustCompile("(?is)([a-z_:][-a-z0-9_:.]*)\\s*=\\s*(?:\\\"([^\\\"]*)\\\"|'([^']*)'|([^\\s>]+))")
	ssCommandPaths   = []string{"/usr/bin/ss", "/usr/sbin/ss", "/bin/ss", "/sbin/ss"}
)

type dockerInspection struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
	State struct {
		PID int `json:"Pid"`
	} `json:"State"`
	NetworkSettings struct {
		Ports map[string][]dockerBinding `json:"Ports"`
	} `json:"NetworkSettings"`
}

type dockerBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type listener struct {
	Address string
	Port    uint16
	Process string
	PIDs    []int
}

type installedEntry struct {
	AppName  string
	Protocol string
	Port     uint16
	Path     string
}

type installedEntryIndex map[uint16][]installedEntry

type probeResult struct {
	WebProbeResult
	Err error
}

func (h *Helper) discovery(writer http.ResponseWriter, request *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ctx, cancel := context.WithTimeout(request.Context(), h.timeout())
	defer cancel()
	items, err := h.discover(ctx)
	if err != nil {
		writeProblem(writer, http.StatusBadGateway, publicCommandError(err))
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]any{"items": items})
}

func (h *Helper) discover(ctx context.Context) ([]app.DiscoveryCandidate, error) {
	registrations, err := h.listRegistrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("read application-center registrations: %w", err)
	}
	installed := h.installedEntries(registrations)
	candidates, dockerPorts, hostContainers := h.dockerCandidates(ctx, installed)
	hostItems, hostErr := h.hostCandidates(ctx, dockerPorts, hostContainers, installed)
	candidates = append(candidates, hostItems...)
	diagnostic := map[string]any{
		"capturedAt": time.Now().UTC().Format(time.RFC3339),
		"items":      candidates,
	}
	if hostErr != nil {
		warning := "host listener scan unavailable: " + publicCommandError(hostErr)
		diagnostic["warnings"] = []string{warning}
		slog.Warn("host listener discovery unavailable", "error", hostErr)
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].DisplayName != candidates[right].DisplayName {
			return strings.ToLower(candidates[left].DisplayName) < strings.ToLower(candidates[right].DisplayName)
		}
		return candidates[left].Port < candidates[right].Port
	})
	if err := h.writeDiagnostic("last-discovery.json", diagnostic); err != nil {
		slog.Warn("failed to write discovery diagnostics", "error", err)
	}
	return candidates, nil
}

func (h *Helper) dockerCandidates(ctx context.Context, installed installedEntryIndex) ([]app.DiscoveryCandidate, map[uint16]bool, []dockerInspection) {
	ports := map[uint16]bool{}
	dockerCLI := h.DockerCLI
	if dockerCLI == "" {
		dockerCLI = "/usr/bin/docker"
	}
	output, err := h.runner().Run(ctx, "", dockerCLI, "ps", "--quiet")
	if err != nil {
		return nil, ports, nil
	}
	identifiers := strings.Fields(string(output))
	items := make([]app.DiscoveryCandidate, 0)
	hostContainers := make([]dockerInspection, 0)
	for _, identifier := range identifiers {
		output, inspectErr := h.runner().Run(ctx, "", dockerCLI, "inspect", identifier)
		if inspectErr != nil {
			continue
		}
		var inspected []dockerInspection
		if json.Unmarshal(output, &inspected) != nil || len(inspected) != 1 {
			continue
		}
		if inspected[0].HostConfig.NetworkMode == "host" {
			hostContainers = append(hostContainers, inspected[0])
		}
		for _, candidate := range candidatesFromDocker(ctx, inspected[0], installed, h.probeWeb) {
			ports[candidate.Port] = true
			items = append(items, candidate)
		}
	}
	return items, ports, hostContainers
}

func (h *Helper) hostCandidates(
	ctx context.Context,
	dockerPorts map[uint16]bool,
	hostContainers []dockerInspection,
	installed installedEntryIndex,
) ([]app.DiscoveryCandidate, error) {
	output, err := h.listenerOutput(ctx)
	if err != nil {
		return nil, err
	}
	listeners := ipv4ReachableListeners(parseListeners(output))
	results := make([]probeResult, len(listeners))
	var probes sync.WaitGroup
	limit := make(chan struct{}, 8)
	for index := range listeners {
		probes.Add(1)
		go func(index int) {
			defer probes.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			result, probeErr := h.probeWebAt(ctx, listeners[index].Address, listeners[index].Port)
			results[index] = probeResult{WebProbeResult: result, Err: probeErr}
		}(index)
	}
	probes.Wait()
	items := make([]app.DiscoveryCandidate, 0, len(listeners))
	for index, item := range listeners {
		if dockerPorts[item.Port] {
			continue
		}
		result := results[index]
		if result.Err != nil {
			continue
		}
		protocol, title := result.Protocol, result.Title
		displayName := item.Process
		if displayName == "" {
			displayName = "本地 Web 服务"
		}
		if title != "" {
			displayName = title
		}
		candidate := app.DiscoveryCandidate{
			Key:          fmt.Sprintf("host:%s:%d", item.Address, item.Port),
			DisplayName:  displayName,
			Description:  "宿主机监听端口",
			Protocol:     protocol,
			Port:         item.Port,
			Path:         "/",
			Source:       "host",
			SourceDetail: item.Process,
			Address:      item.Address,
			GroupKey:     fmt.Sprintf("host:%s", item.Process),
			IconURI:      result.IconURI,
		}
		if len(item.PIDs) > 0 {
			candidate.PID = item.PIDs[0]
		}
		if owner, confidence := h.listenerContainer(item, hostContainers); owner != nil {
			name := strings.TrimPrefix(owner.Name, "/")
			candidate.Key = fmt.Sprintf("docker:%s:%s:%d", owner.ID, item.Address, item.Port)
			candidate.Source = "docker"
			candidate.SourceDetail = name
			candidate.Description = owner.Config.Image
			candidate.DisplayName = name
			if title != "" {
				candidate.DisplayName = title
			}
			candidate.GroupKey = "docker:" + owner.ID
			candidate.ContainerID = owner.ID
			candidate.NetworkMode = owner.HostConfig.NetworkMode
			candidate.OwnerConfidence = confidence
			applyWatchCowHint(&candidate, owner.Config.Labels)
		}
		candidate.ExistingApplication = installed.match(protocol, item.Port, "/")
		items = append(items, candidate)
	}
	return items, nil
}

func (h *Helper) listenerOutput(ctx context.Context) ([]byte, error) {
	failures := make([]error, 0, len(ssCommandPaths))
	for _, command := range ssCommandPaths {
		output, err := h.runner().Run(ctx, "", command, "-H", "-ltnp")
		if err == nil {
			return output, nil
		}
		failures = append(failures, fmt.Errorf("%s: %w", command, err))
	}
	return nil, fmt.Errorf("read TCP listeners: %w", errors.Join(failures...))
}

func ipv4ReachableListeners(input []listener) []listener {
	type selectedListener struct {
		item       listener
		nativeIPv4 bool
	}
	selected := make(map[string]selectedListener, len(input))
	for _, item := range input {
		address := strings.Trim(strings.TrimSpace(item.Address), "[]")
		nativeIPv4 := address == "0.0.0.0"
		switch address {
		case "", "*", "0.0.0.0", "::":
			address = "127.0.0.1"
		default:
			ip := net.ParseIP(address)
			if ip == nil || ip.To4() == nil {
				continue
			}
			address = ip.To4().String()
			nativeIPv4 = true
		}
		item.Address = address
		key := net.JoinHostPort(address, strconv.FormatUint(uint64(item.Port), 10))
		current, exists := selected[key]
		if !exists || nativeIPv4 && !current.nativeIPv4 {
			selected[key] = selectedListener{item: item, nativeIPv4: nativeIPv4}
		}
	}
	items := make([]listener, 0, len(selected))
	for _, current := range selected {
		items = append(items, current.item)
	}
	sort.Slice(items, func(left, right int) bool {
		if items[left].Port != items[right].Port {
			return items[left].Port < items[right].Port
		}
		return items[left].Address < items[right].Address
	})
	return items
}

func isIPv4DockerBinding(address string) bool {
	address = strings.Trim(strings.TrimSpace(address), "[]")
	if address == "" {
		return true
	}
	ip := net.ParseIP(address)
	return ip != nil && ip.To4() != nil
}

func candidatesFromDocker(
	ctx context.Context,
	inspected dockerInspection,
	installed installedEntryIndex,
	probe func(context.Context, uint16) (WebProbeResult, error),
) []app.DiscoveryCandidate {
	labels := inspected.Config.Labels
	name := strings.TrimPrefix(inspected.Name, "/")
	if name == "" {
		name = "Docker Web 服务"
	}
	ports := make([]uint16, 0)
	for _, bindings := range inspected.NetworkSettings.Ports {
		for _, binding := range bindings {
			if !isIPv4DockerBinding(binding.HostIP) {
				continue
			}
			port, err := parsePort(binding.HostPort)
			if err == nil {
				ports = append(ports, port)
			}
		}
	}
	sort.Slice(ports, func(left, right int) bool { return ports[left] < ports[right] })
	results := make([]probeResult, len(ports))
	var probes sync.WaitGroup
	limit := make(chan struct{}, 8)
	for index := range ports {
		probes.Add(1)
		go func(index int) {
			defer probes.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			result, probeErr := probe(ctx, ports[index])
			results[index] = probeResult{WebProbeResult: result, Err: probeErr}
		}(index)
	}
	probes.Wait()
	seen := map[uint16]bool{}
	items := make([]app.DiscoveryCandidate, 0, len(ports))
	for index, port := range ports {
		if seen[port] {
			continue
		}
		seen[port] = true
		result := results[index]
		if result.Err != nil {
			continue
		}
		protocol, title := result.Protocol, result.Title
		candidate := app.DiscoveryCandidate{
			Key:          fmt.Sprintf("docker:%s:%d", strings.TrimPrefix(inspected.ID, "/"), port),
			DisplayName:  name,
			Description:  inspected.Config.Image,
			Protocol:     protocol,
			Port:         port,
			Path:         "/",
			Source:       "docker",
			SourceDetail: name,
			Address:      "127.0.0.1",
			GroupKey:     "docker:" + inspected.ID,
			ContainerID:  inspected.ID,
			NetworkMode:  inspected.HostConfig.NetworkMode,
			IconURI:      result.IconURI,
		}
		if title != "" {
			candidate.DisplayName = title
		}
		applyWatchCowHint(&candidate, labels)
		candidate.ExistingApplication = installed.match(candidate.Protocol, port, candidate.Path)
		items = append(items, candidate)
	}
	return items
}

func applyWatchCowHint(candidate *app.DiscoveryCandidate, labels map[string]string) {
	candidate.WatchCow = strings.EqualFold(strings.TrimSpace(labels["watchcow.enable"]), "true")
	if !candidate.WatchCow {
		return
	}
	preferredPort, err := parsePort(labels["watchcow.service_port"])
	if err != nil || preferredPort != candidate.Port {
		return
	}
	candidate.Preferred = true
	if displayName := strings.TrimSpace(labels["watchcow.display_name"]); displayName != "" {
		candidate.DisplayName = displayName
	}
	if protocol := strings.ToLower(strings.TrimSpace(labels["watchcow.protocol"])); protocol == "https" {
		candidate.Protocol = protocol
	}
	if path := strings.TrimSpace(labels["watchcow.path"]); strings.HasPrefix(path, "/") {
		candidate.Path = path
	}
	candidate.IconURI = strings.TrimSpace(labels["watchcow.icon"])
}

func (h *Helper) installedEntries(registrations map[string]registration) installedEntryIndex {
	index := installedEntryIndex{}
	for _, registration := range registrations {
		root, err := h.observedRegistrationPath(registration.AppName, registration.Volume)
		if err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, "ui", "config"))
		if err != nil {
			continue
		}
		var config struct {
			URL map[string]json.RawMessage `json:".url"`
		}
		if json.Unmarshal(body, &config) != nil {
			continue
		}
		for _, raw := range config.URL {
			var entry struct {
				Protocol  string `json:"protocol"`
				URL       string `json:"url"`
				LegacyURL string `json:"path"`
				NoDisplay bool   `json:"noDisplay"`
			}
			if json.Unmarshal(raw, &entry) != nil || entry.NoDisplay {
				continue
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(raw, &fields) != nil {
				continue
			}
			port, err := parseJSONPort(fields["port"])
			if err != nil {
				continue
			}
			path := entry.URL
			if path == "" {
				path = entry.LegacyURL
			}
			if path == "" {
				path = "/"
			}
			index[port] = append(index[port], installedEntry{
				AppName: registration.AppName, Protocol: strings.ToLower(entry.Protocol),
				Port: port, Path: path,
			})
		}
	}
	for port := range index {
		sort.Slice(index[port], func(left, right int) bool {
			return index[port][left].AppName < index[port][right].AppName
		})
	}
	return index
}

func (h *Helper) observedRegistrationPath(appName, fallbackVolume string) (string, error) {
	if !observedAppName.MatchString(appName) {
		return "", errors.New("invalid observed application name")
	}
	if !positiveInteger.MatchString(fallbackVolume) {
		fallbackVolume = "1"
	}
	fallbackRoot := filepath.Join("/vol"+fallbackVolume, "@appcenter")
	registryDir := h.AppRegistryDir
	if registryDir == "" {
		registryDir = "/var/apps"
	}
	link := filepath.Join(registryDir, appName, "target")
	resolved, err := filepath.EvalSymlinks(link)
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Join(fallbackRoot, appName), nil
	}
	if err != nil {
		return "", err
	}
	roots := append([]string(nil), h.AllowedInstallRoots...)
	if len(roots) == 0 {
		roots = []string{fallbackRoot, "/usr/local/apps/@appcenter"}
	}
	resolved = filepath.Clean(resolved)
	for _, root := range roots {
		root = filepath.Clean(root)
		if filepath.IsAbs(root) && resolved == filepath.Join(root, appName) && shellpkg.Within(root, resolved) {
			return resolved, nil
		}
	}
	return "", errors.New("observed application path is outside approved roots")
}

func parseJSONPort(raw json.RawMessage) (uint16, error) {
	if len(raw) == 0 {
		return 0, errors.New("entry has no port")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return parsePort(text)
	}
	var number uint16
	if json.Unmarshal(raw, &number) == nil && number > 0 {
		return number, nil
	}
	return 0, errors.New("entry has invalid port")
}

func (index installedEntryIndex) match(protocol string, port uint16, path string) string {
	entries := index[port]
	for _, entry := range entries {
		if (entry.Protocol == "" || entry.Protocol == protocol) && entry.Path == path {
			return entry.AppName
		}
	}
	if len(entries) > 0 {
		return entries[0].AppName
	}
	return ""
}

func (h *Helper) listenerContainer(item listener, containers []dockerInspection) (*dockerInspection, string) {
	for index := range containers {
		container := &containers[index]
		for _, pid := range item.PIDs {
			if container.State.PID > 0 && pid == container.State.PID {
				return container, "high"
			}
			if body, err := h.processCgroup(pid); err == nil && cgroupContainsContainer(body, container.ID) {
				return container, "high"
			}
			if container.State.PID > 0 {
				listenerNamespace, listenerErr := h.pidNamespace(pid)
				containerNamespace, containerErr := h.pidNamespace(container.State.PID)
				if listenerErr == nil && containerErr == nil && listenerNamespace == containerNamespace {
					return container, "medium"
				}
			}
		}
	}
	return nil, ""
}

func (h *Helper) processCgroup(pid int) ([]byte, error) {
	if h.ReadProcessCgroup != nil {
		return h.ReadProcessCgroup(pid)
	}
	return os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
}

func (h *Helper) pidNamespace(pid int) (string, error) {
	if h.ReadPIDNamespace != nil {
		return h.ReadPIDNamespace(pid)
	}
	return os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "ns", "pid"))
}

func cgroupContainsContainer(body []byte, containerID string) bool {
	containerID = strings.TrimSpace(containerID)
	if len(containerID) < 12 {
		return false
	}
	return strings.Contains(string(body), containerID) ||
		strings.Contains(string(body), containerID[:12])
}

func parseListeners(output []byte) []listener {
	unique := map[string]listener{}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		address, port, err := parseListenerAddress(fields[3])
		if err != nil {
			continue
		}
		key := net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10))
		current := unique[key]
		current.Address = address
		current.Port = port
		if match := listenerProcess.FindStringSubmatch(line); len(match) == 2 && current.Process == "" {
			current.Process = match[1]
		}
		seenPIDs := map[int]bool{}
		for _, pid := range current.PIDs {
			seenPIDs[pid] = true
		}
		for _, match := range listenerPID.FindAllStringSubmatch(line, -1) {
			pid, parseErr := strconv.Atoi(match[1])
			if parseErr == nil && pid > 0 && !seenPIDs[pid] {
				current.PIDs = append(current.PIDs, pid)
				seenPIDs[pid] = true
			}
		}
		sort.Ints(current.PIDs)
		unique[key] = current
	}
	items := make([]listener, 0, len(unique))
	for _, item := range unique {
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Port < items[right].Port })
	return items
}

func parseListenerAddress(value string) (string, uint16, error) {
	if host, portText, err := net.SplitHostPort(value); err == nil {
		port, parseErr := parsePort(portText)
		return strings.Trim(host, "[]"), port, parseErr
	}
	if end := strings.LastIndex(value, ":"); end >= 0 {
		port, err := parsePort(value[end+1:])
		return strings.Trim(value[:end], "[]"), port, err
	}
	return "", 0, fmt.Errorf("listener address has no port")
}

func parsePort(value string) (uint16, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 16)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("invalid port")
	}
	return uint16(parsed), nil
}

func (h *Helper) probeWeb(ctx context.Context, port uint16) (WebProbeResult, error) {
	return h.probeWebAt(ctx, "127.0.0.1", port)
}

func (h *Helper) probeWebAt(ctx context.Context, address string, port uint16) (WebProbeResult, error) {
	if h.WebProbe != nil {
		return h.WebProbe(ctx, port)
	}
	address = probeAddress(address)
	for _, protocol := range []string{"http", "https"} {
		transport := &http.Transport{DisableKeepAlives: true}
		if protocol == "https" {
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- local protocol classification only
		}
		client := &http.Client{
			Timeout:   900 * time.Millisecond,
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		target := protocol + "://" + net.JoinHostPort(address, strconv.FormatUint(uint64(port), 10)) + "/"
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		request.Header.Set("Range", "bytes=0-16383")
		response, err := client.Do(request)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		_ = response.Body.Close()
		title := ""
		if match := titlePattern.FindSubmatch(body); len(match) == 2 {
			title = strings.TrimSpace(string(match[1]))
		}
		if protocol == "http" && plainHTTPToHTTPSPort(response.StatusCode, title, body) {
			continue
		}
		base, parseErr := url.Parse(target)
		if parseErr != nil {
			return WebProbeResult{Protocol: protocol, Title: title}, nil
		}
		iconURI := linkedIconURI(base, body)
		return WebProbeResult{Protocol: protocol, Title: title, IconURI: iconURI}, nil
	}
	return WebProbeResult{}, fmt.Errorf("port %d did not answer HTTP or HTTPS", port)
}

func linkedIconURI(base *url.URL, body []byte) string {
	for _, tag := range linkPattern.FindAll(body, -1) {
		attributes := parseHTMLAttributes(string(tag))
		if !iconRelationship(attributes["rel"]) {
			continue
		}
		if candidate := sameOriginIcon(base, html.UnescapeString(attributes["href"])); candidate != "" {
			return candidate
		}
	}
	return ""
}

func parseHTMLAttributes(tag string) map[string]string {
	attributes := map[string]string{}
	for _, match := range attributePattern.FindAllStringSubmatch(tag, -1) {
		value := ""
		for index := 2; index < len(match); index++ {
			if match[index] != "" {
				value = match[index]
				break
			}
		}
		attributes[strings.ToLower(match[1])] = strings.TrimSpace(value)
	}
	return attributes
}

func iconRelationship(value string) bool {
	for _, relationship := range strings.Fields(strings.ToLower(value)) {
		if relationship == "icon" || relationship == "shortcut" || relationship == "apple-touch-icon" ||
			relationship == "apple-touch-icon-precomposed" {
			return true
		}
	}
	return false
}

func sameOriginIcon(base *url.URL, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	reference, err := url.Parse(value)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != base.Scheme || !strings.EqualFold(resolved.Host, base.Host) || resolved.User != nil {
		return ""
	}
	if resolved.Path == "" {
		return ""
	}
	result := resolved.EscapedPath()
	if resolved.RawQuery != "" {
		result += "?" + resolved.RawQuery
	}
	return result
}

func plainHTTPToHTTPSPort(status int, title string, body []byte) bool {
	if status != http.StatusBadRequest && status != http.StatusUpgradeRequired {
		return false
	}
	message := strings.ToLower(title + " " + string(body))
	return strings.Contains(message, "plain http request was sent to https port") ||
		strings.Contains(message, "client sent an http request to an https server")
}

func probeAddress(address string) string {
	switch strings.TrimSpace(address) {
	case "", "*", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return strings.Trim(address, "[]")
	}
}

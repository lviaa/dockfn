package fnos

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dockfn/dockfn/internal/app"
)

type discoveryRunner struct{}

func (discoveryRunner) Run(_ context.Context, _ string, name string, arguments ...string) ([]byte, error) {
	if name == "/usr/local/bin/appcenter-cli" && len(arguments) == 2 && arguments[0] == "list" && arguments[1] == "--json" {
		return []byte(`[{"appname":"watchcow.demo","volume":"1"}]`), nil
	}
	if name == "/usr/bin/docker" && len(arguments) == 2 && arguments[0] == "ps" && arguments[1] == "--quiet" {
		return []byte("container-one\n"), nil
	}
	if name == "/usr/bin/docker" && len(arguments) == 2 && arguments[0] == "inspect" && arguments[1] == "container-one" {
		return []byte(`[{"Id":"container-one","Name":"/demo","Config":{"Image":"demo:latest","Labels":{"watchcow.enable":"true","watchcow.appname":"watchcow.demo","watchcow.display_name":"Demo","watchcow.service_port":"8080","watchcow.protocol":"https","watchcow.path":"/app","watchcow.icon":"https://example.test/demo.png"}},"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}}}]`), nil
	}
	if name == "/usr/bin/ss" && len(arguments) == 2 && arguments[0] == "-H" && arguments[1] == "-ltnp" {
		return []byte("LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* users:((\"docker-proxy\",pid=1,fd=4))\nLISTEN 0 128 127.0.0.1:9000 0.0.0.0:* users:((\"my-web\",pid=2,fd=5))\n"), nil
	}
	return nil, fmt.Errorf("unexpected command: %s %v", name, arguments)
}

type missingSSRunner struct{}

func (missingSSRunner) Run(_ context.Context, _ string, name string, arguments ...string) ([]byte, error) {
	if name == "/usr/local/bin/appcenter-cli" && len(arguments) == 2 && arguments[0] == "list" && arguments[1] == "--json" {
		return []byte("[]"), nil
	}
	if name == "/usr/bin/docker" && len(arguments) == 2 && arguments[0] == "ps" && arguments[1] == "--quiet" {
		return nil, nil
	}
	return nil, fmt.Errorf("%s is unavailable", name)
}

type dualStackListenerRunner struct{}

func (dualStackListenerRunner) Run(_ context.Context, _ string, name string, arguments ...string) ([]byte, error) {
	if name == "/usr/local/bin/appcenter-cli" && len(arguments) == 2 && arguments[0] == "list" && arguments[1] == "--json" {
		return []byte("[]"), nil
	}
	if name == "/usr/bin/docker" && len(arguments) == 2 && arguments[0] == "ps" && arguments[1] == "--quiet" {
		return nil, nil
	}
	if name == "/usr/bin/ss" && len(arguments) == 2 && arguments[0] == "-H" && arguments[1] == "-ltnp" {
		return []byte("LISTEN 0 4096 *:12212 *:* users:((\"dual-stack-web\",pid=122,fd=7))\n"), nil
	}
	return nil, fmt.Errorf("unexpected command: %s %v", name, arguments)
}

func TestDiscoverFindsReadOnlyDockerAndHostCandidates(t *testing.T) {
	helper := &Helper{
		AppCenterCLI: "/usr/local/bin/appcenter-cli", DockerCLI: "/usr/bin/docker", Runner: discoveryRunner{},
		WebProbe: func(_ context.Context, port uint16) (WebProbeResult, error) {
			if port == 8080 {
				return WebProbeResult{Protocol: "http", IconURI: "/favicon.ico"}, nil
			}
			if port == 9000 {
				return WebProbeResult{Protocol: "http", Title: "Host UI", IconURI: "/favicon.png"}, nil
			}
			return WebProbeResult{}, fmt.Errorf("unexpected probe %d", port)
		},
	}
	items, err := helper.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	if items[0].Source != "docker" || items[0].Protocol != "https" || items[0].Port != 8080 || items[0].ExistingApplication != "" || !items[0].WatchCow || !items[0].Preferred {
		t.Fatalf("unexpected Docker candidate: %#v", items[0])
	}
	if items[1].Source != "host" || items[1].DisplayName != "Host UI" || items[1].Port != 9000 || items[1].SourceDetail != "my-web" {
		t.Fatalf("unexpected host candidate: %#v", items[1])
	}
	if items[0].IconURI != "https://example.test/demo.png" || items[1].IconURI != "/favicon.png" {
		t.Fatalf("discovered icon URIs were not propagated: %#v", items)
	}
}

func TestDiscoverKeepsDualStackListenerReachableOverIPv4(t *testing.T) {
	helper := &Helper{
		AppCenterCLI: "/usr/local/bin/appcenter-cli",
		DockerCLI:    "/usr/bin/docker",
		Runner:       dualStackListenerRunner{},
		WebProbe: func(_ context.Context, port uint16) (WebProbeResult, error) {
			if port != 12212 {
				return WebProbeResult{}, fmt.Errorf("unexpected probe %d", port)
			}
			return WebProbeResult{Protocol: "http", Title: "Dual-stack web"}, nil
		},
	}
	items, err := helper.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Port != 12212 || items[0].Address != "127.0.0.1" {
		t.Fatalf("IPv4-reachable dual-stack listener was lost: %#v", items)
	}
}

func TestDiscoverRecordsAWarningWhenNoApprovedSSPathExists(t *testing.T) {
	dataDir := t.TempDir()
	helper := &Helper{
		AppCenterCLI: "/usr/local/bin/appcenter-cli",
		DockerCLI:    "/usr/bin/docker",
		DataDir:      dataDir,
		Runner:       missingSSRunner{},
	}
	items, err := helper.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("unexpected candidates: %#v", items)
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "diagnostics", "last-discovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var diagnostic struct {
		Warnings []string `json:"warnings"`
	}
	if err = json.Unmarshal(body, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if len(diagnostic.Warnings) != 1 || !strings.Contains(diagnostic.Warnings[0], "host listener scan unavailable") {
		t.Fatalf("missing host scan warning: %s", body)
	}
}

func TestProbeWebFallsBackToHTTPSWhenPlainHTTPGetsTLSHint(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("<html><title>Secure service</title></html>"))
	}))
	defer server.Close()

	_, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&Helper{}).probeWebAt(context.Background(), "127.0.0.1", uint16(port))
	if err != nil {
		t.Fatal(err)
	}
	if result.Protocol != "https" || result.Title != "Secure service" {
		t.Fatalf("probe=%s %q, want https Secure service", result.Protocol, result.Title)
	}
}

func TestProbeWebDiscoversSameOriginLinkIcon(t *testing.T) {
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/":
			_, _ = response.Write([]byte(`<html><head><link href="assets/app.png?v=2" rel="shortcut icon"></head><title>Linked</title></html>`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	result, err := (&Helper{}).probeWebAt(context.Background(), "127.0.0.1", uint16(port))
	if err != nil || result.IconURI != "/assets/app.png?v=2" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(requests) != 1 || requests[0] != "/" {
		t.Fatalf("scan fetched the linked icon: %v", requests)
	}
}

func TestProbeWebDoesNotProbeCommonIconsDuringScan(t *testing.T) {
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		if request.URL.Path == "/" {
			_, _ = response.Write([]byte(`<html><script src="/ignored.js"></script><title>Fallback</title></html>`))
			return
		}
		if request.URL.Path == "/favicon.png" {
			_, _ = response.Write([]byte("\x89PNG\r\n\x1a\nimage"))
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	result, err := (&Helper{}).probeWebAt(context.Background(), "127.0.0.1", uint16(port))
	if err != nil || result.IconURI != "" {
		t.Fatalf("result=%#v err=%v requests=%v", result, err, requests)
	}
	if len(requests) != 1 || requests[0] != "/" {
		t.Fatalf("scan issued secondary resource requests: %v", requests)
	}
}

func TestProbeWebRejectsSPAHTMLFallbackAsAnIcon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "image/png")
		_, _ = response.Write([]byte(`<html><title>SPA fallback</title></html>`))
	}))
	defer server.Close()
	_, portText, _ := net.SplitHostPort(server.Listener.Addr().String())
	port, _ := strconv.ParseUint(portText, 10, 16)
	result, err := (&Helper{}).probeWebAt(context.Background(), "127.0.0.1", uint16(port))
	if err != nil || result.IconURI != "" {
		t.Fatalf("HTML fallback was treated as an icon: result=%#v err=%v", result, err)
	}
}

func TestParseListenersIgnoresInvalidEntriesAndDeduplicatesPorts(t *testing.T) {
	items := parseListeners([]byte("LISTEN 0 128 [::]:8080 [::]:* users:((\"web\",pid=1,fd=5))\nLISTEN 0 128 0.0.0.0:8080 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:* 0.0.0.0:*\n"))
	var ipv6 listener
	for _, item := range items {
		if item.Address == "::" {
			ipv6 = item
		}
	}
	if len(items) != 2 || ipv6.Port != 8080 || ipv6.Process != "web" ||
		len(ipv6.PIDs) != 1 || ipv6.PIDs[0] != 1 {
		t.Fatalf("items=%#v", items)
	}
}

func TestParseListenersDoesNotTruncateLargeInventories(t *testing.T) {
	var input strings.Builder
	for port := 8000; port < 8040; port++ {
		fmt.Fprintf(&input, "LISTEN 0 128 127.0.0.1:%d 0.0.0.0:* users:((\"web\",pid=%d,fd=5))\n", port, port)
	}
	items := parseListeners([]byte(input.String()))
	if len(items) != 40 {
		t.Fatalf("listeners were truncated: got %d, want 40", len(items))
	}
}

func TestIPv4ReachableListenersKeepsWildcardButRejectsIPv6OnlyAddress(t *testing.T) {
	items := ipv4ReachableListeners([]listener{
		{Address: "::", Port: 12212, Process: "dual-stack"},
		{Address: "::1", Port: 12213, Process: "ipv6-only"},
		{Address: "0.0.0.0", Port: 12212, Process: "native-ipv4"},
		{Address: "127.0.0.1", Port: 12214, Process: "loopback"},
	})
	if len(items) != 2 {
		t.Fatalf("items=%#v", items)
	}
	if items[0].Address != "127.0.0.1" || items[0].Port != 12212 || items[0].Process != "native-ipv4" {
		t.Fatalf("wildcard normalization did not prefer the native IPv4 listener: %#v", items[0])
	}
	if items[1].Address != "127.0.0.1" || items[1].Port != 12214 {
		t.Fatalf("IPv4 loopback listener changed unexpectedly: %#v", items[1])
	}
}

func TestWatchCowContainerMatchesOnlyTheInstalledEntryPort(t *testing.T) {
	var inspected []dockerInspection
	body := `[{
		"Id":"npm","Name":"/nginx-proxy-manager",
		"Config":{"Image":"jc21/nginx-proxy-manager:2.15.1","Labels":{
			"watchcow.enable":"true","watchcow.display_name":"nginx proxy manager",
			"watchcow.service_port":"20081","watchcow.protocol":"http","watchcow.path":"/"
		}},
		"HostConfig":{"NetworkMode":"1panel-network"},
		"NetworkSettings":{"Ports":{
			"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8880"}],
			"81/tcp":[{"HostIp":"0.0.0.0","HostPort":"20081"}],
			"443/tcp":[{"HostIp":"0.0.0.0","HostPort":"8443"}]
		}}
	}]`
	if err := json.Unmarshal([]byte(body), &inspected); err != nil {
		t.Fatal(err)
	}
	index := installedEntryIndex{
		20081: {{AppName: "watchcow.nginx-proxy-manager", Protocol: "http", Port: 20081, Path: "/"}},
	}
	items := candidatesFromDocker(context.Background(), inspected[0], index, successfulProbe)
	if len(items) != 3 {
		t.Fatalf("items=%#v", items)
	}
	for _, item := range items {
		if item.Port == 20081 {
			if item.ExistingApplication != "watchcow.nginx-proxy-manager" || !item.Preferred {
				t.Fatalf("preferred WatchCow entry was not matched: %#v", item)
			}
		} else if item.ExistingApplication != "" || item.Preferred {
			t.Fatalf("unregistered sibling port inherited application state: %#v", item)
		}
	}
}

func TestSqMusicMatchesInstalledPortWithoutAppNameInference(t *testing.T) {
	var inspected []dockerInspection
	body := `[{
		"Id":"sqmusic","Name":"/sqmusic_web",
		"Config":{"Image":"sqmusic-web:latest","Labels":{
			"watchcow.enable":"true","watchcow.display_name":"SqMusic",
			"watchcow.service_port":"8100","watchcow.protocol":"http","watchcow.path":"/"
		}},
		"NetworkSettings":{"Ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8100"}]}}
	}]`
	if err := json.Unmarshal([]byte(body), &inspected); err != nil {
		t.Fatal(err)
	}
	index := installedEntryIndex{
		8100: {{AppName: "watchcow.sqmusic-web", Protocol: "http", Port: 8100, Path: "/"}},
	}
	items := candidatesFromDocker(context.Background(), inspected[0], index, successfulProbe)
	if len(items) != 1 || items[0].ExistingApplication != "watchcow.sqmusic-web" {
		t.Fatalf("SqMusic installed entry was not matched by port: %#v", items)
	}
}

func TestDockerCandidatesIgnoreIPv6OnlyPublishedBindings(t *testing.T) {
	var inspected []dockerInspection
	body := `[{
		"Id":"dual-stack","Name":"/dual-stack",
		"Config":{"Image":"example/dual-stack:latest"},
		"NetworkSettings":{"Ports":{
			"80/tcp":[{"HostIp":"::","HostPort":"8080"}],
			"81/tcp":[{"HostIp":"0.0.0.0","HostPort":"8081"}]
		}}
	}]`
	if err := json.Unmarshal([]byte(body), &inspected); err != nil {
		t.Fatal(err)
	}
	items := candidatesFromDocker(context.Background(), inspected[0], nil, successfulProbe)
	if len(items) != 1 || items[0].Port != 8081 {
		t.Fatalf("IPv6-only Docker binding entered IPv4 discovery: %#v", items)
	}
}

func TestHostNetworkListenerIsAttributedByCgroup(t *testing.T) {
	containerID := strings.Repeat("a", 64)
	container := dockerInspection{ID: containerID, Name: "/host-web"}
	container.HostConfig.NetworkMode = "host"
	container.State.PID = 123
	helper := &Helper{
		ReadProcessCgroup: func(pid int) ([]byte, error) {
			if pid != 456 {
				return nil, fmt.Errorf("unexpected pid %d", pid)
			}
			return []byte("0::/system.slice/docker-" + containerID + ".scope\n"), nil
		},
		ReadPIDNamespace: func(int) (string, error) {
			return "", fmt.Errorf("namespace fallback should not be needed")
		},
	}
	owner, confidence := helper.listenerContainer(
		listener{Address: "0.0.0.0", Port: 8088, Process: "web", PIDs: []int{456}},
		[]dockerInspection{container},
	)
	if owner == nil || owner.ID != containerID || confidence != "high" {
		t.Fatalf("owner=%#v confidence=%q", owner, confidence)
	}
}

func TestWatchCowHintOnlyAppliesToServicePort(t *testing.T) {
	labels := map[string]string{
		"watchcow.enable":       "true",
		"watchcow.display_name": "Preferred Web",
		"watchcow.service_port": "20081",
		"watchcow.protocol":     "https",
		"watchcow.path":         "/admin",
		"watchcow.icon":         "https://example.test/icon.png",
	}
	sibling := app.DiscoveryCandidate{DisplayName: "Container 8880", Protocol: "http", Port: 8880, Path: "/"}
	applyWatchCowHint(&sibling, labels)
	if !sibling.WatchCow || sibling.Preferred || sibling.DisplayName != "Container 8880" || sibling.Path != "/" {
		t.Fatalf("sibling port inherited WatchCow entry fields: %#v", sibling)
	}
	preferred := app.DiscoveryCandidate{DisplayName: "Container 20081", Protocol: "http", Port: 20081, Path: "/"}
	applyWatchCowHint(&preferred, labels)
	if !preferred.Preferred || preferred.DisplayName != "Preferred Web" || preferred.Protocol != "https" ||
		preferred.Path != "/admin" || preferred.IconURI == "" {
		t.Fatalf("preferred port did not receive WatchCow hint: %#v", preferred)
	}
}

func successfulProbe(_ context.Context, port uint16) (WebProbeResult, error) {
	return WebProbeResult{Protocol: "http", Title: fmt.Sprintf("Web %d", port)}, nil
}

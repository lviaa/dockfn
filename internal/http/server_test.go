package apihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dockfn/dockfn/internal/app"
	"github.com/dockfn/dockfn/internal/auth"
	"github.com/dockfn/dockfn/internal/config"
)

type httpBuilder struct {
	staging string
}

func (b httpBuilder) Render(_ context.Context, spec app.AppSpec) (app.BuildSource, error) {
	directory := filepath.Join(b.staging, spec.ID+"-http", "source")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return app.BuildSource{}, err
	}
	return app.BuildSource{Directory: directory}, nil
}

type httpPlatform struct {
	mu        sync.Mutex
	installed map[string]bool
}

type httpDiscoverer struct{}

func (httpDiscoverer) Discover(context.Context) ([]app.DiscoveryCandidate, error) {
	return []app.DiscoveryCandidate{{
		Key: "docker:demo:8080", DisplayName: "Demo", Protocol: "http", Port: 8080, Path: "/", Source: "docker",
	}}, nil
}

func (p *httpPlatform) Install(_ context.Context, _ string, spec app.AppSpec, source string) (app.InstalledArtifact, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	path := filepath.Join(filepath.Dir(source), spec.AppName+".fpk")
	if err := os.WriteFile(path, []byte("fpk"), 0o600); err != nil {
		return app.InstalledArtifact{}, err
	}
	p.installed[spec.AppName] = true
	return app.InstalledArtifact{FPKPath: path}, nil
}
func (p *httpPlatform) Remove(_ context.Context, spec app.AppSpec) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.installed, spec.AppName)
	return nil
}
func (p *httpPlatform) Installed(_ context.Context, spec app.AppSpec) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.installed[spec.AppName], nil
}

func httpFixture(t *testing.T) (http.Handler, *config.FileStore) {
	t.Helper()
	data := t.TempDir()
	staging := filepath.Join(data, "staging")
	repository, err := config.OpenFileStore(data)
	if err != nil {
		t.Fatal(err)
	}
	discoveryStore, err := config.OpenDiscoveryStore(data)
	if err != nil {
		t.Fatal(err)
	}
	settingsStore, err := config.OpenSettingsStore(data)
	if err != nil {
		t.Fatal(err)
	}
	service := &app.Service{
		Repo: repository, Builder: httpBuilder{staging: staging},
		Platform:   &httpPlatform{installed: map[string]bool{}},
		Discoverer: httpDiscoverer{},
		DataDir:    data, StagingDir: staging,
		Probe:    func(context.Context, uint16) error { return nil },
		NewID:    func() (string, error) { return "012345abcdef", nil },
		Settings: settingsStore,
	}
	server := &Server{
		Apps: service, Discovery: discoveryStore, Settings: settingsStore,
		Version: "test", HelperAvailable: func() bool { return true },
		ClearDiagnostics: func(context.Context) error { return nil },
	}
	return server.Handler(), repository
}

func TestDiscoveryIgnoredKeysPersistThroughAPI(t *testing.T) {
	handler, _ := httpFixture(t)
	response := adminRequest(handler, http.MethodPut, "/api/discovery/ignored", []byte(`{"keys":[""]}`))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid ignored status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodPut, "/api/discovery/ignored", []byte(`{"keys":["docker:demo:8080"]}`))
	if response.Code != http.StatusOK {
		t.Fatalf("replace ignored status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodGet, "/api/discovery/ignored", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "docker:demo:8080") {
		t.Fatalf("list ignored status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodPost, "/api/discovery/scan", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "ignoredKeys") {
		t.Fatalf("scan ignored status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGlobalSettingsValidatePersistAndDriveCreation(t *testing.T) {
	handler, repository := httpFixture(t)
	response := adminRequest(handler, http.MethodPut, "/api/settings", []byte(`{
		"entryPrefixTemplate":"{id}",
		"defaultOpenType":"url",
		"defaultAllUsers":false,
		"autoScanOnCreate":true,
		"showDockFNBadge":true
	}`))
	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"field":"entryPrefixTemplate"`) {
		t.Fatalf("unsafe settings status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodPut, "/api/settings", []byte(`{
		"entryPrefixTemplate":"app-{id}",
		"defaultOpenType":"iframe",
		"defaultAllUsers":true,
		"autoScanOnCreate":false,
		"showDockFNBadge":false
	}`))
	if response.Code != http.StatusOK {
		t.Fatalf("save settings status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodGet, "/api/settings", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"entryPrefixTemplate":"app-{id}"`) {
		t.Fatalf("get settings status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodPost, "/api/apps", []byte(`{
		"displayName":"Panel",
		"protocol":"http",
		"port":8080,
		"path":"/",
		"allUsers":true
	}`))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := repository.Get(context.Background(), "012345abcdef")
	if err != nil || stored.AppName != "app-panel" || stored.EntryID != "panel" || stored.ShowDockFNBadge == nil || *stored.ShowDockFNBadge {
		t.Fatalf("stored application=%#v err=%v", stored, err)
	}
}

func TestSuggestEntryIDUsesPinyinAndCurrentTemplate(t *testing.T) {
	handler, _ := httpFixture(t)
	response := adminRequest(handler, http.MethodPost, "/api/entry-ids/suggest", []byte(`{"displayName":"飞牛应用坞 DockFN"}`))
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"entryId":"fei-niu-ying-yong-wu-dockfn"`) ||
		!strings.Contains(response.Body.String(), `"appName":"dkfn.fei-niu-ying-yong-wu-dockfn"`) {
		t.Fatalf("suggest status=%d body=%s", response.Code, response.Body.String())
	}
}

func adminRequest(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		request.Header.Set("Content-Type", "application/json")
	}
	request = auth.WithActor(request, auth.Actor{ID: "admin", Admin: true})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestRawFnOSHeadersCannotForgeAdmin(t *testing.T) {
	handler, _ := httpFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	request.Header.Set("X-Trim-Username", "forged")
	request.Header.Set("X-Trim-Isadmin", "true")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("forged headers returned %d", response.Code)
	}
}

func TestSynchronousLifecycleAndManualDiscoveryRoutes(t *testing.T) {
	handler, repository := httpFixture(t)
	createBody := []byte(`{"displayName":"Photos","protocol":"http","port":8080,"path":"/","allUsers":true}`)
	response := adminRequest(handler, http.MethodPost, "/api/apps", createBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if items, _ := repository.List(context.Background()); len(items) != 1 {
		t.Fatalf("synchronous create did not persist: %#v", items)
	} else if items[0].OpenType != "url" {
		t.Fatalf("omitted openType did not default to url: %#v", items[0])
	}
	for _, route := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/apps", http.StatusOK},
		{http.MethodPost, "/api/discovery/scan", http.StatusOK},
		{http.MethodGet, "/api/apps/012345abcdef", http.StatusOK},
		{http.MethodPost, "/api/apps/012345abcdef/check", http.StatusOK},
		{http.MethodPost, "/api/apps/012345abcdef/refresh-icon", http.StatusOK},
		{http.MethodPost, "/api/apps/012345abcdef/repair", http.StatusOK},
		{http.MethodGet, "/api/system/status", http.StatusOK},
		{http.MethodGet, "/api/system/diagnostics", http.StatusOK},
		{http.MethodDelete, "/api/system/diagnostics", http.StatusNoContent},
	} {
		response = adminRequest(handler, route.method, route.path, nil)
		if response.Code != route.status {
			t.Fatalf("%s %s status=%d body=%s", route.method, route.path, response.Code, response.Body.String())
		}
	}
	updateBody := []byte(`{"displayName":"New Photos","openType":"url","protocol":"https","port":8443,"path":"/photos","allUsers":false}`)
	response = adminRequest(handler, http.MethodPut, "/api/apps/012345abcdef", updateBody)
	if response.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", response.Code, response.Body.String())
	}
	if stored, _ := repository.Get(context.Background(), "012345abcdef"); stored.OpenType != "url" {
		t.Fatalf("URL openType was not persisted: %#v", stored)
	}
	response = adminRequest(handler, http.MethodPost, "/api/apps/012345abcdef/rollback", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", response.Code, response.Body.String())
	}
	response = adminRequest(handler, http.MethodDelete, "/api/apps/012345abcdef", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestClearDiagnosticsInvokesPrivilegedOperation(t *testing.T) {
	called := false
	server := &Server{ClearDiagnostics: func(context.Context) error {
		called = true
		return nil
	}}
	response := adminRequest(server.Handler(), http.MethodDelete, "/api/system/diagnostics", nil)
	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("clear diagnostics status=%d called=%v body=%s", response.Code, called, response.Body.String())
	}
}

func TestCreateAcceptsCustomEntryPrefix(t *testing.T) {
	handler, repository := httpFixture(t)
	body := []byte(`{"displayName":"Blinko","entryPrefix":"blinko","protocol":"http","port":1111,"path":"/","allUsers":true}`)
	response := adminRequest(handler, http.MethodPost, "/api/apps", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	stored, err := repository.Get(context.Background(), "012345abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if stored.AppName != "dkfn.blinko" {
		t.Fatalf("custom appName=%q", stored.AppName)
	}
}

func TestCreateRejectsNumericEntryPrefix(t *testing.T) {
	handler, _ := httpFixture(t)
	body := []byte(`{"displayName":"1Panel2","entryPrefix":"1panel2","protocol":"http","port":12212,"path":"/","allUsers":true}`)
	response := adminRequest(handler, http.MethodPost, "/api/apps", body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"field":"entryPrefix"`) {
		t.Fatalf("numeric entry prefix did not return a field error: %s", response.Body.String())
	}
}

func TestRemovedPlatformRoutesStayAbsent(t *testing.T) {
	handler, _ := httpFixture(t)
	for _, path := range []string{
		"/api/jobs", "/api/candidates", "/api/audit-events",
		"/api/label-templates", "/api/auth/session", "/api/backup",
	} {
		response := adminRequest(handler, http.MethodGet, path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s returned %d", path, response.Code)
		}
	}
}

func TestProblemHasStableCodeAndSuggestion(t *testing.T) {
	handler, _ := httpFixture(t)
	response := adminRequest(handler, http.MethodPost, "/api/apps", []byte(`{"displayName":"","protocol":"ftp","port":0,"path":"bad","allUsers":false}`))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "VALIDATION_FAILED" || problem.Suggestion == "" || len(problem.Fields) == 0 {
		t.Fatalf("incomplete problem response: %#v", problem)
	}
}

func TestInvalidIconURIIsAFieldValidationError(t *testing.T) {
	handler, _ := httpFixture(t)
	body := []byte(`{"displayName":"Photos","protocol":"http","port":8080,"path":"/","allUsers":true,"iconUri":"file:///definitely/missing/icon.ico"}`)
	response := adminRequest(handler, http.MethodPost, "/api/apps", body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "VALIDATION_FAILED" || len(problem.Fields) != 1 || problem.Fields[0].Field != "iconUri" {
		t.Fatalf("unexpected problem response: %#v", problem)
	}
}

func TestIconPreviewRouteValidatesInput(t *testing.T) {
	handler, _ := httpFixture(t)
	body := []byte(`{"iconUri":"","protocol":"http","port":8080}`)
	response := adminRequest(handler, http.MethodPost, "/api/icons/preview", body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "VALIDATION_FAILED" || len(problem.Fields) != 1 ||
		problem.Fields[0].Field != "iconUri" {
		t.Fatalf("unexpected preview problem: %#v", problem)
	}
}

func TestIconDiscoverRouteValidatesInput(t *testing.T) {
	handler, _ := httpFixture(t)
	body := []byte(`{"protocol":"http","port":8080,"path":"panel"}`)
	response := adminRequest(handler, http.MethodPost, "/api/icons/discover", body)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "VALIDATION_FAILED" || len(problem.Fields) != 1 ||
		problem.Fields[0].Field != "path" {
		t.Fatalf("unexpected discovery problem: %#v", problem)
	}
}

func TestNonAdminIsForbidden(t *testing.T) {
	handler, _ := httpFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/apps", nil)
	request = auth.WithActor(request, auth.Actor{ID: "user", Admin: false})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin status=%d", response.Code)
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	handler, _ := httpFixture(t)
	response := adminRequest(handler, http.MethodPost, "/api/apps", []byte(`{"displayName":"X","protocol":"http","port":80,"path":"/","allUsers":false,"jobId":"forbidden"}`))
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown field status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNotFoundMapsRepositoryError(t *testing.T) {
	handler, _ := httpFixture(t)
	response := adminRequest(handler, http.MethodGet, "/api/apps/abcdef012345", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("not found status=%d body=%s", response.Code, response.Body.String())
	}
	var problem Problem
	_ = json.Unmarshal(response.Body.Bytes(), &problem)
	if problem.Code != "APP_NOT_FOUND" {
		t.Fatalf("unexpected problem %#v", problem)
	}
}

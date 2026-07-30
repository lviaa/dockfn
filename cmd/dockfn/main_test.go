package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dockfn/dockfn/internal/auth"
)

func TestFnOSGatewayStripsPrefixAndIdentityHeaders(t *testing.T) {
	var observedPath string
	var observedActor auth.Actor
	var observedAuthorization string
	next := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		observedPath = request.URL.Path
		observedActor, _ = auth.ActorFrom(request.Context())
		observedAuthorization = request.Header.Get("Authorization")
		_ = json.NewEncoder(writer).Encode(map[string]bool{"ok": true})
	})
	request := httptest.NewRequest(http.MethodGet, "/app/dockfn/api/apps", nil)
	request.Header.Set("X-Trim-Username", "administrator")
	request.Header.Set("X-Trim-Isadmin", "true")
	request.Header.Set("Authorization", "secret")
	response := httptest.NewRecorder()
	fnOSGateway(next, "/app/dockfn").ServeHTTP(response, request)
	if response.Code != http.StatusOK || observedPath != "/api/apps" {
		t.Fatalf("status=%d path=%q", response.Code, observedPath)
	}
	if observedActor.ID != "administrator" || !observedActor.Admin {
		t.Fatalf("unexpected actor %#v", observedActor)
	}
	if observedAuthorization != "" {
		t.Fatal("authorization header crossed the gateway seam")
	}
}

func TestFnOSGatewayRejectsPathsOutsideApplicationPrefix(t *testing.T) {
	response := httptest.NewRecorder()
	fnOSGateway(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("outside-prefix request reached application")
	}), "/app/dockfn").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/apps", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestFnOSGatewayDoesNotCreateAnEmptyActor(t *testing.T) {
	response := httptest.NewRecorder()
	handler := fnOSGateway(auth.RequireAdmin(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("request without identity reached the protected handler")
	})), "/app/dockfn")
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app/dockfn/api/apps", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", response.Code)
	}
}

func TestFnOSGatewayRedirectsApplicationRoot(t *testing.T) {
	response := httptest.NewRecorder()
	fnOSGateway(http.NotFoundHandler(), "/app/dockfn").ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, "/app/dockfn?tab=apps", nil),
	)
	if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != "/app/dockfn/?tab=apps" {
		t.Fatalf("status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
}

func TestRunVersion(t *testing.T) {
	if code := run([]string{"dockfn", "version"}); code != 0 {
		t.Fatalf("version exited %d", code)
	}
}

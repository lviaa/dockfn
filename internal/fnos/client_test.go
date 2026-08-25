package fnos

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/dockfn/dockfn/internal/app"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientClearDiagnosticsUsesFixedDeleteEndpoint(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodDelete || request.URL.Path != "/v1/diagnostics" {
			t.Fatalf("unexpected helper request %s %s", request.Method, request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}}
	if err := client.ClearDiagnostics(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClientDiscoverClassifiesUnavailableHelper(t *testing.T) {
	client := &Client{http: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("helper socket is unavailable")
	})}}
	_, err := client.Discover(context.Background())
	if !errors.Is(err, app.ErrDiscoveryUnavailable) {
		t.Fatalf("discovery error = %v, want ErrDiscoveryUnavailable", err)
	}
	if strings.Contains(err.Error(), "helper socket is unavailable") {
		t.Fatalf("discovery error exposed the raw helper connection failure: %v", err)
	}
}

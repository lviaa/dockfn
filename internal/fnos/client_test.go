package fnos

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
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

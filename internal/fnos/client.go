package fnos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dockfn/dockfn/internal/app"
	"github.com/dockfn/dockfn/internal/package"
)

type Client struct {
	Socket     string
	StagingDir string
	DataDir    string
	Timeout    time.Duration
	http       *http.Client
}

type actionRequest struct {
	AppName        string `json:"appName"`
	SourceRelative string `json:"sourceRelative,omitempty"`
}

type actionResponse struct {
	FPKRelative string `json:"fpkRelative,omitempty"`
	InstallPath string `json:"installPath,omitempty"`
	Message     string `json:"message"`
}

type ownership struct {
	AppName        string `json:"appName"`
	GeneratedBy    string `json:"generatedBy"`
	InstallPath    string `json:"installPath,omitempty"`
	ArtifactSHA256 string `json:"artifactSha256,omitempty"`
}

func (c *Client) Install(ctx context.Context, action string, spec app.AppSpec, source string) (app.InstalledArtifact, error) {
	if action != "install" && action != "update" {
		return app.InstalledArtifact{}, errors.New("unsupported fnOS action")
	}
	if !shellpkg.Within(c.StagingDir, source) {
		return app.InstalledArtifact{}, errors.New("package source escaped staging")
	}
	relative, err := filepath.Rel(c.StagingDir, source)
	if err != nil || filepath.IsAbs(relative) || strings.HasPrefix(relative, "..") {
		return app.InstalledArtifact{}, errors.New("package source is not a safe staging child")
	}
	var response actionResponse
	if err = c.call(ctx, action, actionRequest{AppName: spec.AppName, SourceRelative: filepath.ToSlash(relative)}, &response); err != nil {
		return app.InstalledArtifact{}, err
	}
	fpk := filepath.Join(c.StagingDir, filepath.FromSlash(response.FPKRelative))
	if !shellpkg.Within(c.StagingDir, fpk) || filepath.Ext(fpk) != ".fpk" {
		return app.InstalledArtifact{}, errors.New("helper returned an unsafe artifact path")
	}
	return app.InstalledArtifact{FPKPath: fpk}, nil
}

func (c *Client) Remove(ctx context.Context, spec app.AppSpec) error {
	if !app.IsOwnedAppName(spec.AppName) {
		return errors.New("unsafe appName")
	}
	return c.call(ctx, "remove", actionRequest{AppName: spec.AppName}, &actionResponse{})
}

func (c *Client) Installed(_ context.Context, spec app.AppSpec) (bool, error) {
	marker, err := readOwnership(c.DataDir, spec.AppName)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if marker.GeneratedBy != "dockfn" || marker.AppName != spec.AppName {
		return false, errors.New("invalid ownership marker")
	}
	if marker.InstallPath == "" {
		return false, nil
	}
	info, err := os.Stat(marker.InstallPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (c *Client) Available() bool {
	info, err := os.Stat(c.Socket)
	return err == nil && info.Mode()&os.ModeSocket != 0
}

func (c *Client) Discover(ctx context.Context) ([]app.DiscoveryCandidate, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/discovery", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("fnOS helper unavailable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &problem) != nil || problem.Message == "" {
			problem.Message = "fnOS helper rejected discovery"
		}
		return nil, errors.New(problem.Message)
	}
	var result struct {
		Items []app.DiscoveryCandidate `json:"items"`
	}
	if err = json.Unmarshal(body, &result); err != nil {
		return nil, errors.New("fnOS helper returned invalid discovery data")
	}
	return result.Items, nil
}

func (c *Client) ClearDiagnostics(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, "http://unix/v1/diagnostics", nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("fnOS helper unavailable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &problem) != nil || problem.Message == "" {
			problem.Message = "fnOS helper rejected diagnostic clearing"
		}
		return errors.New(problem.Message)
	}
	return nil
}

func (c *Client) call(ctx context.Context, action string, input actionRequest, output *actionResponse) error {
	if action != "install" && action != "update" && action != "remove" {
		return errors.New("invalid helper action")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/"+action, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return fmt.Errorf("fnOS helper unavailable: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(responseBody, &problem) != nil || problem.Message == "" {
			problem.Message = "fnOS helper rejected the operation"
		}
		return errors.New(problem.Message)
	}
	if err = json.Unmarshal(responseBody, output); err != nil {
		return errors.New("fnOS helper returned an invalid response")
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.http != nil {
		return c.http
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 50 * time.Second
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", c.Socket)
	}}
	c.http = &http.Client{Transport: transport, Timeout: timeout}
	return c.http
}

func readOwnership(dataDir, appName string) (ownership, error) {
	if !app.IsOwnedAppName(appName) {
		return ownership{}, errors.New("unsafe appName")
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "ownership", appName+".json"))
	if err != nil {
		return ownership{}, err
	}
	var marker ownership
	if err = json.Unmarshal(body, &marker); err != nil {
		return ownership{}, err
	}
	return marker, nil
}

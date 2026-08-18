package apihttp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/dockfn/dockfn/internal/app"
	"github.com/dockfn/dockfn/internal/auth"
	"github.com/dockfn/dockfn/internal/diagnostics"
	"github.com/dockfn/dockfn/internal/webui"
)

type Server struct {
	Apps             *app.Service
	Discovery        DiscoveryPreferences
	Settings         SettingsPreferences
	Version          string
	HelperAvailable  func() bool
	Diagnostics      func() diagnostics.Snapshot
	ClearDiagnostics func(context.Context) error
}

type DiscoveryPreferences interface {
	ListIgnored(context.Context) ([]string, error)
	ReplaceIgnored(context.Context, []string) error
}

type SettingsPreferences interface {
	Get(context.Context) (app.Settings, error)
	Replace(context.Context, app.Settings) error
}

type settingsInput struct {
	EntryPrefixTemplate string `json:"entryPrefixTemplate"`
	DefaultOpenType     string `json:"defaultOpenType"`
	DefaultAllUsers     *bool  `json:"defaultAllUsers"`
	AutoScanOnCreate    *bool  `json:"autoScanOnCreate"`
	ShowDockFNBadge     *bool  `json:"showDockFNBadge"`
}

type identitySuggestionInput struct {
	DisplayName string `json:"displayName"`
}

type ignoredCandidatesInput struct {
	Keys []string `json:"keys"`
}

type Problem struct {
	Code       string           `json:"code"`
	Message    string           `json:"message"`
	Suggestion string           `json:"suggestion"`
	RequestID  string           `json:"requestId"`
	Fields     []app.FieldError `json:"fields,omitempty"`
}

func (s *Server) Handler() http.Handler {
	staticFS, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(staticFS))
	api := auth.RequireAdmin(http.HandlerFunc(s.api))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			api.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			http.NotFound(writer, request)
			return
		}
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path != "" {
			if _, statErr := fs.Stat(staticFS, path); statErr == nil {
				files.ServeHTTP(writer, request)
				return
			}
		}
		clone := request.Clone(request.Context())
		clone.URL.Path = "/"
		files.ServeHTTP(writer, clone)
	})
}

func (s *Server) api(writer http.ResponseWriter, request *http.Request) {
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" || len(requestID) > 100 {
		requestID = randomID()
	}
	writer.Header().Set("X-Request-ID", requestID)
	writer.Header().Set("Cache-Control", "no-store")
	path := strings.TrimPrefix(request.URL.Path, "/api")
	switch {
	case path == "/apps" && request.Method == http.MethodGet:
		items, err := s.Apps.List(request.Context())
		payload := map[string]any{"items": items}
		if err == nil && s.Settings != nil {
			settings, settingsErr := s.Settings.Get(request.Context())
			if settingsErr != nil {
				err = settingsErr
			} else {
				payload["settings"] = settings
			}
		}
		s.respond(writer, request, http.StatusOK, payload, err)
	case path == "/settings" && request.Method == http.MethodGet:
		if s.Settings == nil {
			s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such DockFN endpoint.", "Refresh the page and try again.", nil)
			return
		}
		settings, err := s.Settings.Get(request.Context())
		s.respond(writer, request, http.StatusOK, settings, err)
	case path == "/settings" && request.Method == http.MethodPut:
		if s.Settings == nil {
			s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such DockFN endpoint.", "Refresh the page and try again.", nil)
			return
		}
		var input settingsInput
		if !decode(writer, request, &input) {
			return
		}
		missing := make([]app.FieldError, 0, 3)
		if input.DefaultAllUsers == nil {
			missing = append(missing, app.FieldError{Field: "defaultAllUsers", Message: "is required"})
		}
		if input.AutoScanOnCreate == nil {
			missing = append(missing, app.FieldError{Field: "autoScanOnCreate", Message: "is required"})
		}
		if input.ShowDockFNBadge == nil {
			missing = append(missing, app.FieldError{Field: "showDockFNBadge", Message: "is required"})
		}
		if len(missing) > 0 {
			s.respond(writer, request, http.StatusOK, nil, &app.ValidationError{Fields: missing})
			return
		}
		settings := app.Settings{
			EntryPrefixTemplate: input.EntryPrefixTemplate,
			DefaultOpenType:     input.DefaultOpenType,
			DefaultAllUsers:     *input.DefaultAllUsers,
			AutoScanOnCreate:    *input.AutoScanOnCreate,
			ShowDockFNBadge:     *input.ShowDockFNBadge,
		}
		if err := s.Settings.Replace(request.Context(), settings); err != nil {
			s.respond(writer, request, http.StatusOK, nil, err)
			return
		}
		settings, err := s.Settings.Get(request.Context())
		s.respond(writer, request, http.StatusOK, settings, err)
	case path == "/entry-ids/suggest" && request.Method == http.MethodPost:
		var input identitySuggestionInput
		if !decode(writer, request, &input) {
			return
		}
		identity, err := s.Apps.SuggestIdentity(request.Context(), input.DisplayName)
		s.respond(writer, request, http.StatusOK, identity, err)
	case path == "/discovery/scan" && request.Method == http.MethodPost:
		items, err := s.Apps.Discover(request.Context())
		if err != nil {
			s.respond(writer, request, http.StatusOK, nil, err)
			return
		}
		payload := map[string]any{"items": items}
		if s.Discovery != nil {
			keys, listErr := s.Discovery.ListIgnored(request.Context())
			if listErr != nil {
				s.respond(writer, request, http.StatusOK, nil, listErr)
				return
			}
			payload["ignoredKeys"] = keys
		}
		s.respond(writer, request, http.StatusOK, payload, nil)
	case path == "/discovery/ignored" && request.Method == http.MethodGet:
		if s.Discovery == nil {
			s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such DockFN endpoint.", "Refresh the page and try again.", nil)
			return
		}
		keys, err := s.Discovery.ListIgnored(request.Context())
		s.respond(writer, request, http.StatusOK, map[string]any{"keys": keys}, err)
	case path == "/discovery/ignored" && request.Method == http.MethodPut:
		if s.Discovery == nil {
			s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such DockFN endpoint.", "Refresh the page and try again.", nil)
			return
		}
		var input ignoredCandidatesInput
		if !decode(writer, request, &input) {
			return
		}
		err := s.Discovery.ReplaceIgnored(request.Context(), input.Keys)
		if err != nil {
			s.respond(writer, request, http.StatusOK, nil, err)
			return
		}
		keys, err := s.Discovery.ListIgnored(request.Context())
		s.respond(writer, request, http.StatusOK, map[string]any{"keys": keys}, err)
	case path == "/icons/preview" && request.Method == http.MethodPost:
		var input app.IconPreviewInput
		if !decode(writer, request, &input) {
			return
		}
		dataURL, err := s.Apps.PreviewIcon(request.Context(), input)
		s.respond(writer, request, http.StatusOK, map[string]string{"dataUrl": dataURL}, err)
	case path == "/icons/discover" && request.Method == http.MethodPost:
		var input app.IconDiscoverInput
		if !decode(writer, request, &input) {
			return
		}
		result, err := s.Apps.DiscoverIcon(request.Context(), input)
		s.respond(writer, request, http.StatusOK, result, err)
	case path == "/apps" && request.Method == http.MethodPost:
		var input app.Input
		if !decode(writer, request, &input) {
			return
		}
		view, err := s.Apps.Create(request.Context(), input)
		s.respond(writer, request, http.StatusCreated, app.OperationResult{App: view, Code: "CREATED"}, err)
	case path == "/system/status" && request.Method == http.MethodGet:
		items, err := s.Apps.List(request.Context())
		if err != nil {
			s.respond(writer, request, http.StatusOK, nil, err)
			return
		}
		available := false
		if s.HelperAvailable != nil {
			available = s.HelperAvailable()
		}
		s.respond(writer, request, http.StatusOK, map[string]any{
			"product": "DockFN", "version": s.Version, "architecture": runtime.GOARCH,
			"applications": len(items), "helperAvailable": available,
		}, nil)
	case path == "/system/diagnostics" && request.Method == http.MethodGet:
		snapshot := diagnostics.Snapshot{}
		if s.Diagnostics != nil {
			snapshot = s.Diagnostics()
		}
		s.respond(writer, request, http.StatusOK, snapshot, nil)
	case path == "/system/diagnostics" && request.Method == http.MethodDelete:
		if s.ClearDiagnostics == nil {
			s.respond(writer, request, http.StatusNoContent, nil, errors.New("diagnostic clearing is unavailable"))
			return
		}
		s.respond(writer, request, http.StatusNoContent, nil, s.ClearDiagnostics(request.Context()))
	case strings.HasPrefix(path, "/apps/"):
		s.appRoute(writer, request, strings.TrimPrefix(path, "/apps/"))
	default:
		s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such DockFN endpoint.", "Refresh the page and try again.", nil)
	}
}

func (s *Server) appRoute(writer http.ResponseWriter, request *http.Request, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) < 1 || parts[0] == "" || len(parts) > 2 {
		s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such application endpoint.", "Refresh the page and try again.", nil)
		return
	}
	id := parts[0]
	if len(parts) == 1 {
		switch request.Method {
		case http.MethodGet:
			view, err := s.Apps.Get(request.Context(), id)
			s.respond(writer, request, http.StatusOK, view, err)
		case http.MethodPut:
			var input app.Input
			if !decode(writer, request, &input) {
				return
			}
			view, err := s.Apps.Update(request.Context(), id, input)
			s.respond(writer, request, http.StatusOK, app.OperationResult{App: view, Code: "UPDATED"}, err)
		case http.MethodDelete:
			err := s.Apps.Remove(request.Context(), id)
			if err != nil {
				s.respond(writer, request, http.StatusNoContent, nil, err)
				return
			}
			writer.WriteHeader(http.StatusNoContent)
		default:
			s.problem(writer, request, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "This method is not supported.", "Use the DockFN interface.", nil)
		}
		return
	}
	if request.Method != http.MethodPost {
		s.problem(writer, request, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "This action requires POST.", "Use the DockFN interface.", nil)
		return
	}
	switch parts[1] {
	case "check":
		view, err := s.Apps.Check(request.Context(), id)
		s.respond(writer, request, http.StatusOK, view, err)
	case "refresh-icon":
		view, err := s.Apps.RefreshIcon(request.Context(), id)
		s.respond(writer, request, http.StatusOK, app.OperationResult{App: view, Code: "ICON_REFRESHED"}, err)
	case "repair":
		view, err := s.Apps.Repair(request.Context(), id)
		s.respond(writer, request, http.StatusOK, app.OperationResult{App: view, Code: "REPAIRED"}, err)
	case "rollback":
		view, err := s.Apps.Rollback(request.Context(), id)
		s.respond(writer, request, http.StatusOK, app.OperationResult{App: view, Code: "ROLLED_BACK"}, err)
	default:
		s.problem(writer, request, http.StatusNotFound, "NOT_FOUND", "No such application action.", "Refresh the page and try again.", nil)
	}
}

func decode(writer http.ResponseWriter, request *http.Request, output any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 2<<20)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeDecodeProblem(writer, request, err)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeDecodeProblem(writer, request, errors.New("request must contain one JSON object"))
		return false
	}
	return true
}

func writeDecodeProblem(writer http.ResponseWriter, request *http.Request, err error) {
	requestID := writer.Header().Get("X-Request-ID")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusUnprocessableEntity)
	_ = json.NewEncoder(writer).Encode(Problem{
		Code: "VALIDATION_FAILED", Message: "The request body is invalid: " + err.Error(),
		Suggestion: "Check the highlighted fields and submit again.", RequestID: requestID,
	})
}

func (s *Server) respond(writer http.ResponseWriter, request *http.Request, status int, value any, err error) {
	if err != nil {
		s.mapError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(writer).Encode(value)
	}
}

func (s *Server) mapError(writer http.ResponseWriter, request *http.Request, err error) {
	slog.Warn(
		"DockFN API request failed",
		"requestID", writer.Header().Get("X-Request-ID"),
		"method", request.Method,
		"path", request.URL.Path,
		"error", err,
	)
	var validation *app.ValidationError
	switch {
	case errors.As(err, &validation):
		s.problem(writer, request, http.StatusUnprocessableEntity, "VALIDATION_FAILED", "One or more fields are invalid.", "Correct the fields and submit again.", validation.Fields)
	case errors.Is(err, app.ErrNotFound):
		s.problem(writer, request, http.StatusNotFound, "APP_NOT_FOUND", "The application no longer exists.", "Refresh the application list.", nil)
	case errors.Is(err, app.ErrNoRollback):
		s.problem(writer, request, http.StatusConflict, "ROLLBACK_UNAVAILABLE", err.Error(), "Complete one successful update before rolling back.", nil)
	case errors.Is(err, app.ErrTargetOffline):
		s.problem(writer, request, http.StatusUnprocessableEntity, "TARGET_UNAVAILABLE", err.Error(), "Publish a stable host port and verify the service is running.", nil)
	case errors.Is(err, app.ErrRegistration):
		s.problem(writer, request, http.StatusBadGateway, "FNOS_OPERATION_FAILED", err.Error(), "Check DockFN logs and fnOS Application Center, then retry.", nil)
	case errors.Is(err, app.ErrDiscoveryUnavailable):
		s.problem(writer, request, http.StatusServiceUnavailable, "DISCOVERY_UNAVAILABLE", err.Error(), "Confirm the DockFN helper is running, then try again.", nil)
	default:
		s.problem(writer, request, http.StatusInternalServerError, "INTERNAL_ERROR", "DockFN could not complete the operation.", "Check DockFN logs using the request ID.", nil)
	}
}

func (s *Server) problem(writer http.ResponseWriter, request *http.Request, status int, code, message, suggestion string, fields []app.FieldError) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(Problem{
		Code: code, Message: message, Suggestion: suggestion,
		RequestID: writer.Header().Get("X-Request-ID"), Fields: fields,
	})
}

func randomID() string {
	body := make([]byte, 8)
	if _, err := rand.Read(body); err != nil {
		return time.Now().UTC().Format("20060102150405.000")
	}
	return hex.EncodeToString(body)
}

func AdminHandler(handler http.Handler, id string) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler.ServeHTTP(writer, auth.WithActor(request, auth.Actor{ID: id, Admin: true}))
	})
}

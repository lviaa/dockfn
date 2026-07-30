package auth

import (
	"context"
	"encoding/json"
	"net/http"
)

type Actor struct {
	ID    string `json:"id"`
	Admin bool   `json:"admin"`
}

type actorKey struct{}

func WithActor(request *http.Request, actor Actor) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), actorKey{}, actor))
}

func ActorFrom(ctx context.Context) (Actor, bool) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	return actor, ok
}

// RequireAdmin trusts only the actor placed in context by the fnOS Unix-socket
// gateway adapter. Raw identity headers are never inspected here.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		actor, ok := ActorFrom(request.Context())
		if !ok {
			authProblem(writer, http.StatusUnauthorized, "UNAUTHENTICATED", "Open DockFN from the fnOS desktop.")
			return
		}
		if !actor.Admin {
			authProblem(writer, http.StatusForbidden, "ADMIN_REQUIRED", "An fnOS administrator account is required.")
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func authProblem(writer http.ResponseWriter, status int, code, suggestion string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"code": code, "message": http.StatusText(status), "suggestion": suggestion,
	})
}

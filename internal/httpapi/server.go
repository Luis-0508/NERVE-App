package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nerve-labs/nerve/internal/auth"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/inventory"
)

const sessionCookie = "nerve_session"

type Server struct {
	db            *pgxpool.Pool
	auth          *auth.Service
	inventory     *inventory.Repositories
	cfg           config.Config
	logger        *slog.Logger
	startedAt     time.Time
	version       string
	loginMu       sync.Mutex
	loginAttempts map[string]*rateWindow
}

type rateWindow struct {
	started time.Time
	count   int
}

type sessionResponse struct {
	Authenticated bool          `json:"authenticated"`
	User          *userResponse `json:"user,omitempty"`
}

type userResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Role  string `json:"role"`
}

type eventResponse struct {
	ID         string `json:"id"`
	EntityID   string `json:"entityId,omitempty"`
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	Category   string `json:"category"`
	Message    string `json:"message"`
	OccurredAt string `json:"occurredAt"`
}

func New(db *pgxpool.Pool, cfg config.Config, logger *slog.Logger, version string) *Server {
	return &Server{
		db:            db,
		auth:          auth.NewService(db, cfg.SessionTTL),
		inventory:     inventory.NewRepositories(db),
		cfg:           cfg,
		logger:        logger,
		startedAt:     time.Now().UTC(),
		version:       version,
		loginAttempts: make(map[string]*rateWindow),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /api/v1", s.apiRoot)
	mux.HandleFunc("GET /api/v1/{$}", s.apiRoot)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("POST /api/v1/auth/bootstrap", s.bootstrap)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/session", s.session)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("GET /api/v1/devices", s.devices)
	mux.HandleFunc("GET /api/v1/devices/facets", s.deviceFacets)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}", s.deviceDetail)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/interfaces", s.deviceInterfaces)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/ip-addresses", s.deviceIPAddresses)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/services", s.deviceServices)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/relationships", s.deviceRelationships)
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")

		if origin := r.Header.Get("Origin"); origin != "" {
			if !s.originAllowed(r, origin) {
				writeError(w, http.StatusForbidden, "origin_not_allowed", "The request origin is not allowed.")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if isUnsafe(r.Method) && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			writeError(w, http.StatusForbidden, "cross_site_request", "Cross-site state changes are not allowed.")
			return
		}

		next.ServeHTTP(w, r)
		s.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds(), "remote", s.clientAddress(r))
	})
}

func (s *Server) apiRoot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"name":    "NERVE API",
		"status":  "ok",
		"version": s.version,
	})
}

func (s *Server) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if s.db == nil || s.db.Ping(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "PostgreSQL is not ready.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	databaseStatus := "unavailable"
	var latencyMilliseconds *int64
	database := map[string]any{"status": databaseStatus}
	if s.db != nil {
		started := time.Now()
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		err := s.db.Ping(ctx)
		cancel()
		if err == nil {
			databaseStatus = "ok"
			latency := time.Since(started).Milliseconds()
			latencyMilliseconds = &latency
		}
	}
	database["status"] = databaseStatus
	if latencyMilliseconds != nil {
		database["latencyMs"] = *latencyMilliseconds
	}
	state := "ok"
	if databaseStatus != "ok" {
		state = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        state,
		"version":       s.version,
		"uptimeSeconds": int64(time.Since(s.startedAt).Seconds()),
		"database":      database,
		"time":          time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	if !s.databaseAvailable(w) {
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, token, expiresAt, err := s.auth.Bootstrap(r.Context(), input.Email, input.Password, s.clientAddress(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAlreadyInitialized):
			writeError(w, http.StatusConflict, "already_initialized", err.Error())
		case errors.Is(err, auth.ErrInvalidEmail), errors.Is(err, auth.ErrPasswordPolicy):
			writeError(w, http.StatusUnprocessableEntity, "invalid_setup", err.Error())
		default:
			s.logger.Error("administrator setup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "setup_failed", "The administrator account could not be created.")
		}
		return
	}
	s.setSessionCookie(w, token, expiresAt)
	writeJSON(w, http.StatusCreated, authenticatedResponse(user))
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.databaseAvailable(w) {
		return
	}
	address := s.clientAddress(r)
	if !s.allowLogin(address) {
		w.Header().Set("Retry-After", "900")
		writeError(w, http.StatusTooManyRequests, "login_rate_limited", "Too many sign-in attempts. Try again in 15 minutes.")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, token, expiresAt, err := s.auth.Login(r.Context(), input.Email, input.Password, address)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", auth.ErrInvalidCredentials.Error())
			return
		}
		s.logger.Error("login failed", "error", err)
		writeError(w, http.StatusInternalServerError, "login_failed", "The sign-in request could not be completed.")
		return
	}
	s.clearLoginAttempts(address)
	s.setSessionCookie(w, token, expiresAt)
	writeJSON(w, http.StatusOK, authenticatedResponse(user))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	user, token, ok := s.requireSession(w, r)
	if !ok {
		return
	}
	if err := s.auth.Logout(r.Context(), token, user, s.clientAddress(r)); err != nil {
		s.logger.Error("logout failed", "error", err, "user_id", user.ID)
		writeError(w, http.StatusInternalServerError, "logout_failed", "The session could not be closed.")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}
	user, err := s.auth.Session(r.Context(), cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}
	writeJSON(w, http.StatusOK, authenticatedResponse(user))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	limit := 25
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 100.")
			return
		}
		limit = parsed
	}
	entityID := r.URL.Query().Get("entityId")
	if entityID != "" && !isUUID(entityID) {
		writeError(w, http.StatusBadRequest, "invalid_entity_id", "Entity ID must be a UUID.")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id::text, COALESCE(entity_id::text, ''), type, severity, category, message, occurred_at
		FROM events
		WHERE (NULLIF($2, '') IS NULL OR entity_id = NULLIF($2, '')::uuid)
		ORDER BY occurred_at DESC, id DESC LIMIT $1`, limit, entityID)
	if err != nil {
		s.logger.Error("query events", "error", err)
		writeError(w, http.StatusInternalServerError, "events_unavailable", "Events could not be loaded.")
		return
	}
	defer rows.Close()
	items := make([]eventResponse, 0)
	for rows.Next() {
		var item eventResponse
		var occurred time.Time
		if err := rows.Scan(&item.ID, &item.EntityID, &item.Type, &item.Severity, &item.Category, &item.Message, &occurred); err != nil {
			s.logger.Error("scan event", "error", err)
			writeError(w, http.StatusInternalServerError, "events_unavailable", "Events could not be loaded.")
			return
		}
		item.OccurredAt = occurred.UTC().Format(time.RFC3339Nano)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireSession(w, r); !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "Realtime streaming is unavailable through this proxy.")
		return
	}
	connection, err := s.db.Acquire(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "stream_unavailable", "A realtime database connection could not be opened.")
		return
	}
	defer connection.Release()
	if _, err := connection.Exec(r.Context(), `LISTEN nerve_events`); err != nil {
		writeError(w, http.StatusInternalServerError, "stream_unavailable", "The event stream could not be initialized.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()

	for {
		waitContext, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		notification, err := connection.Conn().WaitForNotification(waitContext)
		cancel()
		if err == nil {
			fmt.Fprintf(w, "event: infrastructure\ndata: %s\n\n", notification.Payload)
			flusher.Flush()
			continue
		}
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
			continue
		}
		s.logger.Warn("event stream ended", "error", err)
		return
	}
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (auth.User, string, bool) {
	if !s.databaseAvailable(w) {
		return auth.User{}, "", false
	}
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to access this resource.")
		return auth.User{}, "", false
	}
	user, err := s.auth.Session(r.Context(), cookie.Value)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_required", "The session is missing or expired.")
		return auth.User{}, "", false
	}
	return user, cookie.Value, true
}

func (s *Server) databaseAvailable(w http.ResponseWriter) bool {
	if s.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "PostgreSQL is not configured.")
		return false
	}
	return true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expiresAt, HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode})
}

func (s *Server) allowLogin(address string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	window, exists := s.loginAttempts[address]
	if !exists || now.Sub(window.started) >= 15*time.Minute {
		s.loginAttempts[address] = &rateWindow{started: now, count: 1}
		return true
	}
	window.count++
	return window.count <= 8
}

func (s *Server) clearLoginAttempts(address string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, address)
	s.loginMu.Unlock()
}

func authenticatedResponse(user auth.User) sessionResponse {
	return sessionResponse{Authenticated: true, User: &userResponse{ID: user.ID, Email: user.Email, Role: user.Role}}
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) clientAddress(r *http.Request) string {
	if s.cfg.TrustProxy {
		if forwarded := net.ParseIP(r.Header.Get("X-Real-IP")); forwarded != nil {
			return forwarded.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) originAllowed(r *http.Request, origin string) bool {
	normalizedOrigin, ok := normalizeOrigin(origin)
	if !ok {
		return false
	}
	for _, configured := range strings.Split(s.cfg.AllowedOrigin, ",") {
		if normalized, valid := normalizeOrigin(strings.TrimSpace(configured)); valid && normalized == normalizedOrigin {
			return true
		}
	}
	if !s.cfg.TrustProxy {
		return false
	}
	forwardedProto := firstForwardedValue(r.Header.Get("X-Forwarded-Proto"))
	forwardedHost := firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
	if forwardedProto == "" || forwardedHost == "" {
		return false
	}
	proxyOrigin, valid := normalizeOrigin(forwardedProto + "://" + forwardedHost)
	return valid && proxyOrigin == normalizedOrigin
}

func normalizeOrigin(value string) (string, bool) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), true
}

func firstForwardedValue(value string) string {
	return strings.TrimSpace(strings.Split(value, ",")[0])
}

func isUnsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

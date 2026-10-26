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
	"github.com/nerve-labs/nerve/internal/agentproto"
	"github.com/nerve-labs/nerve/internal/agents"
	"github.com/nerve-labs/nerve/internal/auth"
	"github.com/nerve-labs/nerve/internal/config"
	"github.com/nerve-labs/nerve/internal/devices"
	"github.com/nerve-labs/nerve/internal/discovery"
	"github.com/nerve-labs/nerve/internal/integrations/ntopng"
	"github.com/nerve-labs/nerve/internal/inventory"
	"github.com/nerve-labs/nerve/internal/remote"
	"github.com/nerve-labs/nerve/internal/traffic"
)

const sessionCookie = "nerve_session"

type Server struct {
	db             *pgxpool.Pool
	auth           *auth.Service
	inventory      *inventory.Repositories
	discovery      *discovery.Repository
	agents         *agents.Repository
	identity       *devices.Service
	commands       *remote.Service
	remote         *remote.Registry
	eventHub       *eventHub
	cfg            config.Config
	logger         *slog.Logger
	startedAt      time.Time
	version        string
	traffic        traffic.Source
	sensor         *traffic.SensorStore
	loginMu        sync.Mutex
	loginAttempts  map[string]*rateWindow
	enrollAttempts map[string]*rateWindow

	// agentLifecycleHooks run inside the revoke/remove transaction.
	agentLifecycleHooks []agents.LifecycleHook
	maintenanceTasks    []func(context.Context) error
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
	ID          string   `json:"id"`
	Email       string   `json:"email"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
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
	locations, geoErr := traffic.LoadCities(cfg.CityDatabase)
	if geoErr != nil {
		logger.Warn("local city database unavailable; using known country representatives only")
	}
	if cfg.TrafficCaptureMode == "windows-native" {
		country, err := traffic.LoadCountries(cfg.CountryDatabase)
		if err != nil {
			logger.Warn("local country fallback database unavailable; city lookup remains independent")
		}
		sensor := traffic.NewSensor(cfg.SensorToken, country, parseTrafficCIDRs(cfg.TrafficLocalCIDRs)...)
		server := NewWithTraffic(db, cfg, logger, version, traffic.WithLocations(sensor, locations))
		server.sensor = sensor
		return server
	}
	return NewWithTraffic(db, cfg, logger, version, traffic.WithLocations(ntopng.New(ntopng.Config{URL: cfg.NtopngURL, Username: cfg.NtopngUsername, Password: cfg.NtopngPassword, InterfaceID: cfg.NtopngInterfaceID, Timeout: cfg.NtopngTimeout, LocalCIDRs: parseTrafficCIDRs(cfg.TrafficLocalCIDRs)}), locations))
}

// SensorHandler is deliberately not mounted on Handler/the web listener.
func (s *Server) SensorHandler() http.Handler {
	if s.sensor == nil {
		return http.NotFoundHandler()
	}
	return s.sensor.Handler()
}

func NewWithTraffic(db *pgxpool.Pool, cfg config.Config, logger *slog.Logger, version string, source traffic.Source) *Server {
	server := &Server{
		db:             db,
		auth:           auth.NewService(db, cfg.SessionTTL),
		inventory:      inventory.NewRepositories(db),
		discovery:      discovery.NewRepository(db),
		agents:         agents.NewRepository(db),
		identity:       devices.NewService(db),
		eventHub:       newEventHub(db, logger),
		cfg:            cfg,
		logger:         logger,
		startedAt:      time.Now().UTC(),
		version:        version,
		traffic:        source,
		loginAttempts:  make(map[string]*rateWindow),
		enrollAttempts: make(map[string]*rateWindow),
	}
	if cfg.CoreSigningKey != "" {
		if key, err := agentproto.ParseCoreSigningKey(cfg.CoreSigningKey); err == nil {
			server.agents.SetCoreSigningKey(key)
		} else {
			logger.Error("invalid NERVE_CORE_SIGNING_KEY; the agent API is disabled")
		}
	}
	server.commands = remote.NewService(db)
	server.commands.SetResultRetention(cfg.CommandResultRetention)
	server.remote = remote.NewRegistry(remote.NativeProvider{Agents: server.agents})
	server.agentLifecycleHooks = append(server.agentLifecycleHooks, server.commands.CancelForAgent)
	server.maintenanceTasks = append(server.maintenanceTasks, server.commands.Sweep)
	return server
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", s.live)
	mux.HandleFunc("GET /health/ready", s.ready)
	mux.HandleFunc("GET /health/discovery", s.discoveryReady)
	mux.HandleFunc("GET /api/v1", s.apiRoot)
	mux.HandleFunc("GET /api/v1/{$}", s.apiRoot)
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/traffic/live", s.liveTraffic)
	mux.HandleFunc("POST /api/v1/auth/bootstrap", s.bootstrap)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/auth/session", s.session)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("GET /api/v1/devices", s.devices)
	mux.HandleFunc("GET /api/v1/devices/facets", s.deviceFacets)
	mux.HandleFunc("GET /api/v1/devices/vocabulary", s.deviceVocabulary)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}", s.deviceDetail)
	mux.HandleFunc("POST /api/v1/devices/{deviceID}/metadata", s.updateDeviceMetadata)
	mux.HandleFunc("POST /api/v1/devices/{deviceID}/trust", s.setDeviceTrust)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/identity", s.deviceIdentity)
	mux.HandleFunc("GET /api/v1/devices/duplicates", s.deviceDuplicates)
	mux.HandleFunc("POST /api/v1/devices/duplicates/dismiss", s.dismissDuplicate)
	mux.HandleFunc("POST /api/v1/devices/{deviceID}/merge", s.mergeDevice)
	mux.HandleFunc("POST /api/v1/devices/{deviceID}/split", s.splitDevice)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/interfaces", s.deviceInterfaces)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/ip-addresses", s.deviceIPAddresses)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/services", s.deviceServices)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/relationships", s.deviceRelationships)
	mux.HandleFunc("GET /api/v1/discovery", s.discoveryState)
	mux.HandleFunc("POST /api/v1/discovery/target", s.saveDiscoveryTarget)
	mux.HandleFunc("POST /api/v1/discovery/scans", s.queueDiscoveryScan)
	mux.HandleFunc("POST /api/v1/discovery/agent/heartbeat", s.discoveryAgentHeartbeat)
	mux.HandleFunc("POST /api/v1/discovery/agent/jobs/claim", s.discoveryAgentClaim)
	mux.HandleFunc("POST /api/v1/discovery/agent/jobs/{scanID}/report", s.discoveryAgentReport)
	mux.HandleFunc("POST /api/v1/discovery/agent/jobs/{scanID}/complete", s.discoveryAgentComplete)
	mux.HandleFunc("GET /api/v1/stream", s.stream)
	mux.HandleFunc("GET /api/v1/audit", s.auditLog)
	mux.HandleFunc("GET /api/v1/agents", s.listAgents)
	mux.HandleFunc("GET /api/v1/agents/{agentID}", s.agentDetail)
	mux.HandleFunc("POST /api/v1/agents/{agentID}/revoke", s.revokeAgent)
	mux.HandleFunc("POST /api/v1/agents/{agentID}/remove", s.removeAgent)
	mux.HandleFunc("POST /api/v1/agents/{agentID}/approve", s.approveAgent)
	mux.HandleFunc("GET /api/v1/agent-enrollments", s.listEnrollments)
	mux.HandleFunc("POST /api/v1/agent-enrollments", s.createEnrollment)
	mux.HandleFunc("POST /api/v1/agent-enrollments/{enrollmentID}/revoke", s.revokeEnrollment)
	mux.HandleFunc("GET /api/v1/agent-deployment-keys", s.listDeploymentKeys)
	mux.HandleFunc("POST /api/v1/agent-deployment-keys", s.createDeploymentKey)
	mux.HandleFunc("POST /api/v1/agent-deployment-keys/{keyID}/revoke", s.revokeDeploymentKey)
	// Agent-authenticated API: Ed25519-signed requests only, never browser sessions.
	mux.HandleFunc("POST /api/v1/agent/enroll", s.coreSigned(s.agentEnroll))
	mux.HandleFunc("POST /api/v1/agent/identity", s.agentEndpoint(4<<10, s.agentIdentity))
	mux.HandleFunc("POST /api/v1/agent/heartbeat", s.agentEndpoint(agentproto.MaxEnrollRequestBytes, s.agentHeartbeat))
	mux.HandleFunc("POST /api/v1/agents/{agentID}/association", s.updateAgentAssociation)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/remote", s.deviceRemote)
	mux.HandleFunc("GET /api/v1/devices/{deviceID}/commands", s.listDeviceCommands)
	mux.HandleFunc("GET /api/v1/agents/{agentID}/commands", s.listAgentCommands)
	mux.HandleFunc("POST /api/v1/agents/{agentID}/commands", s.createCommand)
	mux.HandleFunc("GET /api/v1/remote/commands/{commandID}", s.commandDetail)
	mux.HandleFunc("POST /api/v1/remote/commands/{commandID}/cancel", s.cancelCommand)
	mux.HandleFunc("POST /api/v1/agent/commands/claim", s.agentEndpoint(4<<10, s.agentClaimCommand))
	mux.HandleFunc("POST /api/v1/agent/commands/{commandID}/start", s.agentEndpoint(4<<10, s.agentStartCommand))
	mux.HandleFunc("POST /api/v1/agent/commands/{commandID}/result", s.agentEndpoint(agentproto.MaxResultRequestBytes, s.agentSubmitResult))
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

	events, unsubscribe := s.eventHub.subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "event: ready\ndata: {}\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-events:
			fmt.Fprintf(w, "event: infrastructure\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
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
	return s.allowAttempt(s.loginAttempts, address, 8)
}

// allowAttempt applies a fixed 15-minute window per client address.
func (s *Server) allowAttempt(attempts map[string]*rateWindow, address string, limit int) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	for key, window := range attempts {
		if now.Sub(window.started) >= 15*time.Minute {
			delete(attempts, key)
		}
	}
	window, exists := attempts[address]
	if !exists {
		attempts[address] = &rateWindow{started: now, count: 1}
		return true
	}
	window.count++
	return window.count <= limit
}

func (s *Server) clearLoginAttempts(address string) {
	s.loginMu.Lock()
	delete(s.loginAttempts, address)
	s.loginMu.Unlock()
}

func authenticatedResponse(user auth.User) sessionResponse {
	return sessionResponse{Authenticated: true, User: &userResponse{ID: user.ID, Email: user.Email, Role: user.Role, Permissions: permissionList(user.Permissions)}}
}

func decodeJSON(r *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("invalid JSON request: %w", err)
	}
	// Exactly one JSON value: reject trailing data such as `{"a":1}garbage`.
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid JSON request: unexpected data after the JSON value")
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

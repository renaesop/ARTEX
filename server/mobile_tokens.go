package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
)

const mobileTokenPrefix = "artex_app_"

type mobileTokenContextKey struct{}

var mobileTaskPath = regexp.MustCompile(`^/api/tasks/[1-9][0-9]*$`)
var mobileChatPath = regexp.MustCompile(`^/api/tasks/[1-9][0-9]*/chat/(status|stop)$`)
var mobileControlPath = regexp.MustCompile(`^/api/tasks/[1-9][0-9]*/control$`)
var mobileDetailPath = regexp.MustCompile(`^/api/exploration/(activity|findings)/[1-9][0-9]*$`)
var mobileNumericID = regexp.MustCompile(`^[1-9][0-9]*$`)

func newMobileSecret() (string, string, []byte, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", "", nil, err
	}
	token := mobileTokenPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
	sum := sha256.Sum256([]byte(token))
	// Public identifier is independent from the verifier and secret.
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", "", nil, err
	}
	return token, hex.EncodeToString(id[:]), sum[:], nil
}

func mobileTokenAllowed(r *http.Request) bool {
	p, m := r.URL.Path, r.Method
	if r.URL.RawPath != "" && r.URL.RawPath != p {
		return false
	}
	if p == "/api/mobile-tokens/self" {
		return m == "GET" || m == "DELETE"
	}
	if p == "/api/tasks" {
		return m == "GET" || m == "POST"
	}
	if mobileTaskPath.MatchString(p) {
		return m == "GET" || m == "PATCH" || m == "DELETE"
	}
	if mobileControlPath.MatchString(p) {
		return m == "POST"
	}
	if mobileChatPath.MatchString(p) {
		return (strings.HasSuffix(p, "/status") && m == "GET") || (strings.HasSuffix(p, "/stop") && m == "POST")
	}
	if p == "/api/chat" {
		return m == "POST" && mobileNumericID.MatchString(r.URL.Query().Get("task"))
	}
	if m != "GET" {
		return false
	}
	if p == "/api/exploration/findings" || strings.HasPrefix(p, "/api/exploration/findings/") && mobileDetailPath.MatchString(p) {
		return true
	}
	if mobileDetailPath.MatchString(p) || p == "/api/exploration/intents" || p == "/api/exploration/main-sessions" || p == "/api/exploration/activity/history" || p == "/api/exploration/activity/stream" {
		return mobileNumericID.MatchString(r.URL.Query().Get("task"))
	}
	return p == "/api/health"
}

// Device credentials can remove task execution context, never cascade into stored
// findings/evidence. Reject omitted or unknown cleanup flags, rather than relying
// on a server version's default deletion behavior.
func validateMobileDelete(r *http.Request) bool {
	if r.Method != "DELETE" || !mobileTaskPath.MatchString(r.URL.Path) {
		return true
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(body) > 4096 {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var flags map[string]bool
	if json.Unmarshal(body, &flags) != nil || len(flags) != 5 {
		return false
	}
	for _, name := range []string{"delete_assets", "delete_traffic", "delete_files", "delete_findings", "delete_llm_records"} {
		value, present := flags[name]
		if !present || value {
			return false
		}
	}
	return true
}

func (s *Server) registerMobileTokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/mobile-tokens", s.createMobileToken)
	mux.HandleFunc("GET /api/mobile-tokens", s.listMobileTokens)
	mux.HandleFunc("DELETE /api/mobile-tokens/{id}", s.revokeMobileToken)
	mux.HandleFunc("GET /api/mobile-tokens/self", s.mobileTokenSelf)
	mux.HandleFunc("DELETE /api/mobile-tokens/self", s.mobileTokenSelf)
}

func (s *Server) requireMobileToken(w http.ResponseWriter, r *http.Request, next http.Handler, token string) {
	w.Header().Set("Cache-Control", "no-store")
	// No query tokens or cookies: prevent long-lived secrets entering URLs/logs.
	if r.Header.Get("Authorization") != "Bearer "+token || len(token) != len(mobileTokenPrefix)+43 {
		writeErr(w, 401, "invalid device credential")
		return
	}
	if !mobileTokenAllowed(r) || !validateMobileDelete(r) {
		writeErr(w, 403, "this App token is not allowed to perform this operation")
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if !ok {
		writeErr(w, 401, "invalid device credential")
		return
	}
	sum := sha256.Sum256([]byte(token))
	record, err := pg.ValidateMobileToken(r.Context(), sum[:], hash)
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if record == nil {
		writeErr(w, 401, "device credential is expired or revoked")
		return
	}
	ctx := context.WithValue(r.Context(), mobileTokenContextKey{}, record)
	if strings.HasSuffix(r.URL.Path, "/stream") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, record.ExpiresAt)
		defer cancel()
		// Existing SSE connections also lose access after revocation/password change.
		go func() {
			tick := time.NewTicker(15 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					currentHash, present, e := pg.GetSetting(authPassKey)
					if e != nil || !present {
						cancel()
						return
					}
					current, e := pg.ValidateMobileToken(ctx, sum[:], currentHash)
					if e != nil || current == nil {
						cancel()
						return
					}
				}
			}
		}()
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) freshAdmin(r *http.Request) bool {
	if _, ok := r.Context().Value(mobileTokenContextKey{}).(*db.MobileToken); ok {
		return false
	}
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(extractToken(r), claims, func(t *jwt.Token) (any, error) { return s.jwtKey, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	return err == nil && token.Valid && claims.Subject == "ARTEX" && claims.IssuedAt != nil && time.Since(claims.IssuedAt.Time) >= 0 && time.Since(claims.IssuedAt.Time) <= 5*time.Minute
}

func (s *Server) createMobileToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.freshAdmin(r) {
		writeErr(w, 401, "sign in again before issuing an App token")
		return
	}
	var input struct {
		Name string `json:"name"`
		Days int    `json:"expires_in_days"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		writeErr(w, 400, "invalid request")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Days == 0 {
		input.Days = 90
	}
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 80 || input.Days < 1 || input.Days > 90 {
		writeErr(w, 400, "name and expiry (1–90 days) are required")
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	hash, ok, err := pg.GetSetting(authPassKey)
	if err != nil || !ok {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	secret, id, digest, err := newMobileSecret()
	if err != nil {
		writeErr(w, 500, "cannot issue device credential")
		return
	}
	token, err := pg.CreateMobileToken(r.Context(), id, input.Name, digest, hash, time.Now().Add(time.Duration(input.Days)*24*time.Hour))
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 201, map[string]any{"id": token.ID, "name": token.Name, "token": secret, "expires_at": token.ExpiresAt, "scope": "mobile-control", "idle_expiry_days": 30})
}

func (s *Server) listMobileTokens(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pg := s.pg(w)
	if pg == nil {
		return
	}
	list, err := pg.ListMobileTokens(r.Context())
	if err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}
func (s *Server) revokeMobileToken(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	if err := pg.RevokeMobileToken(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) mobileTokenSelf(w http.ResponseWriter, r *http.Request) {
	record, ok := r.Context().Value(mobileTokenContextKey{}).(*db.MobileToken)
	if !ok {
		writeErr(w, 400, "use the App token to inspect or revoke this device")
		return
	}
	if r.Method == "DELETE" {
		pg := s.pg(w)
		if pg == nil {
			return
		}
		if err := pg.RevokeMobileToken(r.Context(), record.ID); err != nil {
			writeErr(w, 503, errDataSourceUnavailable)
			return
		}
		writeJSON(w, 200, map[string]bool{"revoked": true})
		return
	}
	writeJSON(w, 200, record)
}

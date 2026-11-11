package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMobileHTTPAuthAndSelfRevocation(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Skipf("postgres unavailable: %v", err)
	}
	defer m.Close()
	if err = m.pg.SetSetting(authPassKey, "mobile-integration-password-generation"); err != nil {
		t.Fatal(err)
	}
	s := &Server{m: m, jwtKey: []byte("01234567890123456789012345678901")}
	mux := http.NewServeMux()
	s.registerMobileTokenRoutes(mux)
	mux.HandleFunc("GET /api/tasks", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"tasks": []any{}}) })
	handler := s.requireAuth(mux)
	admin, err := signJWT(s.jwtKey)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	issued := call("POST", "/api/mobile-tokens", admin, `{"name":"Test iPhone","expires_in_days":2}`)
	if issued.Code != 201 {
		t.Fatalf("issuance status=%d", issued.Code)
	}
	var result struct {
		Token string `json:"token"`
		ID    string `json:"id"`
	}
	if json.Unmarshal(issued.Body.Bytes(), &result) != nil || result.Token == "" {
		t.Fatal("missing credential")
	}
	defer m.pg.Exec(`DELETE FROM mobile_tokens WHERE id=$1`, result.ID)
	if got := call("GET", "/api/tasks", result.Token, ""); got.Code != 200 {
		t.Fatalf("read status=%d", got.Code)
	}
	if got := call("GET", "/api/settings", result.Token, ""); got.Code != 403 {
		t.Fatal("settings not blocked")
	}
	if got := call("POST", "/api/mobile-tokens", result.Token, `{"name":"escalate"}`); got.Code != 403 {
		t.Fatal("child credential issuance not blocked")
	}
	if got := call("GET", "/api/tasks?token="+result.Token, "", ""); got.Code != 401 {
		t.Fatal("query credential accepted")
	}
	metadata := call("GET", "/api/mobile-tokens/self", result.Token, "")
	if metadata.Code != 200 || strings.Contains(metadata.Body.String(), result.Token) || strings.Contains(metadata.Body.String(), "password_version") || strings.Contains(metadata.Body.String(), "token_hash") {
		t.Fatal("metadata leaked secret or failed")
	}
	if got := call("DELETE", "/api/mobile-tokens/self", result.Token, ""); got.Code != 200 {
		t.Fatal("revocation failed")
	}
	if got := call("GET", "/api/tasks", result.Token, ""); got.Code != 401 {
		t.Fatal("revoked credential accepted")
	}
}

func TestMobileTokenSecretAndVerifier(t *testing.T) {
	first, id, digest, err := newMobileSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, id2, _, err := newMobileSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(mobileTokenPrefix)+43 || len(id) != 32 || first == second || id == id2 {
		t.Fatal("invalid entropy/identifier")
	}
	want := sha256.Sum256([]byte(first))
	if hex.EncodeToString(want[:]) != hex.EncodeToString(digest) {
		t.Fatal("verifier mismatch")
	}
}

func TestMobileTokenRouteAllowlist(t *testing.T) {
	cases := []struct {
		method, path string
		allow        bool
	}{
		{"GET", "/api/tasks", true}, {"POST", "/api/tasks", true}, {"PATCH", "/api/tasks/52", true},
		{"POST", "/api/tasks/52/control", true}, {"GET", "/api/tasks/52/chat/status", true}, {"POST", "/api/tasks/52/chat/stop", true},
		{"GET", "/api/exploration/findings?task_id=52&page=1", true}, {"GET", "/api/exploration/findings/108", true},
		{"GET", "/api/exploration/activity/history?task=52&session=main:2", true}, {"GET", "/api/exploration/activity/stream?task=52", true},
		{"POST", "/api/chat?task=52", true}, {"DELETE", "/api/mobile-tokens/self", true},
		{"POST", "/api/chat", false}, {"GET", "/api/exploration/activity/stream", false},
		{"POST", "/api/mobile-tokens", false}, {"GET", "/api/mobile-tokens", false}, {"DELETE", "/api/mobile-tokens/someone-else", false},
		{"GET", "/api/settings", false}, {"PUT", "/api/settings", false}, {"POST", "/api/active", false},
		{"POST", "/api/auth/change-password", false}, {"GET", "/api/commands", false},
		{"GET", "/api/files?path=jwt.key", false}, {"POST", "/api/task-archives/2/restore", false},
		{"GET", "/api/tasks/52/credentials", false}, {"PATCH", "/api/exploration/findings/108", false},
		{"GET", "/api/tasks/%35%32", false}, {"GET", "/api/tasks/../settings", false},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if got := mobileTokenAllowed(r); got != tc.allow {
				t.Fatalf("allowed=%v want %v", got, tc.allow)
			}
		})
	}
}

func TestMobileDeleteMustExplicitlyRetainAllEvidence(t *testing.T) {
	good := `{"delete_assets":false,"delete_traffic":false,"delete_files":false,"delete_findings":false,"delete_llm_records":false}`
	for _, body := range []string{`{}`, `null`, strings.Replace(good, `"delete_findings":false`, `"delete_findings":true`, 1), strings.Repeat("x", 4097), strings.Replace(good, `"delete_assets":false,`, "", 1)} {
		if validateMobileDelete(httptest.NewRequest("DELETE", "/api/tasks/52", strings.NewReader(body))) {
			t.Fatal("unsafe deletion accepted")
		}
	}
	r := httptest.NewRequest("DELETE", "/api/tasks/52", strings.NewReader(good))
	if !validateMobileDelete(r) {
		t.Fatal("safe deletion rejected")
	}
	var restored map[string]bool
	if json.NewDecoder(r.Body).Decode(&restored) != nil || len(restored) != 5 {
		t.Fatal("request body not restored")
	}
}

func TestMobileTokenIssuanceRequiresRecentAdminLogin(t *testing.T) {
	s := &Server{jwtKey: []byte("01234567890123456789012345678901")}
	for _, tc := range []struct {
		age   time.Duration
		valid bool
	}{{time.Minute, true}, {10 * time.Minute, false}, {-time.Minute, false}} {
		claims := jwt.RegisteredClaims{Subject: "ARTEX", IssuedAt: jwt.NewNumericDate(time.Now().Add(-tc.age)), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtKey)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/mobile-tokens", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		if s.freshAdmin(r) != tc.valid {
			t.Fatalf("age=%v", tc.age)
		}
	}
}

package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"noters/internal/auth"
	"noters/internal/storage"
)

func authRequest(h http.Handler, method, path, body string, cookie *http.Cookie, csrf bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if csrf {
		r.Header.Set("X-Noters-CSRF", "1")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthenticationLifecycle(t *testing.T) {
	repo := &fakeRepository{sessions: map[string]storage.User{}}
	h := handler(repo)
	w := authRequest(h, "POST", "/api/auth/register", `{"username":" Alice ","password":"a long password 🌱"}`, nil, true)
	if w.Code != 201 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge != 86400 || !auth.ValidToken(cookie.Value) {
		t.Fatalf("unsafe cookie: %#v", cookie)
	}
	user := repo.users["alice"]
	if user.PasswordHash == "a long password 🌱" || !auth.VerifyPassword("a long password 🌱", user.PasswordHash) {
		t.Fatal("password was not hashed")
	}
	if strings.Contains(w.Body.String(), user.PasswordHash) || strings.Contains(w.Body.String(), cookie.Value) {
		t.Fatal("response leaked credentials")
	}
	if _, exists := repo.sessions[auth.TokenHash(cookie.Value)]; !exists {
		t.Fatal("session must store the token hash")
	}
	if w = authRequest(h, "GET", "/api/auth/me", "", cookie, false); w.Code != 200 {
		t.Fatal("session did not authenticate")
	}
	w = authRequest(h, "GET", "/api/notes", "", cookie, false)
	if w.Code != 200 || repo.userID != user.ID {
		t.Fatal("notes were not scoped to authenticated user")
	}
	w = authRequest(h, "POST", "/api/notes", `{"title":"private"}`, cookie, true)
	if w.Code != 201 || repo.userID != user.ID {
		t.Fatal("note did not receive session owner")
	}
	w = authRequest(h, "POST", "/api/auth/logout", "", cookie, false)
	if w.Code != 403 {
		t.Fatal("logout without CSRF header must fail")
	}
	w = authRequest(h, "POST", "/api/auth/logout", "", cookie, true)
	if w.Code != 204 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not clear cookie")
	}
	if w = authRequest(h, "GET", "/api/auth/me", "", cookie, false); w.Code != 401 {
		t.Fatal("logged out session remains valid")
	}
	wrong := authRequest(h, "POST", "/api/auth/login", `{"username":"alice","password":"incorrect"}`, nil, true)
	unknown := authRequest(h, "POST", "/api/auth/login", `{"username":"unknown","password":"incorrect"}`, nil, true)
	if wrong.Code != 401 || unknown.Code != 401 || wrong.Body.String() != unknown.Body.String() {
		t.Fatal("login errors reveal account existence")
	}
	if w = authRequest(h, "POST", "/api/auth/login", `{"username":"ALICE","password":"a long password 🌱"}`, nil, true); w.Code != 200 {
		t.Fatal("login failed")
	}
	if w = authRequest(h, "POST", "/api/auth/register", `{"username":"alice","password":"another password"}`, nil, true); w.Code != 409 {
		t.Fatal("duplicate login accepted")
	}
}

func TestProtectedRoutesAndSessionFailures(t *testing.T) {
	repo := &fakeRepository{}
	h := handler(repo)
	for _, route := range []struct{ method, path string }{
		{"GET", "/api/notes"}, {"GET", "/api/notes/1"}, {"POST", "/api/notes"}, {"PUT", "/api/notes/1"}, {"DELETE", "/api/notes/1"},
	} {
		w := authRequest(h, route.method, route.path, `{"title":"private"}`, nil, true)
		if w.Code != 401 {
			t.Fatalf("%s %s is unprotected: %d", route.method, route.path, w.Code)
		}
	}
	if repo.calls != 0 {
		t.Fatal("anonymous writes reached repository")
	}
	w := authRequest(h, "GET", "/api/notes", "", &http.Cookie{Name: cookieName, Value: "invalid"}, false)
	if w.Code != 401 {
		t.Fatal("invalid token accepted")
	}
	repo.sessionErr = storage.ErrUnauthenticated
	w = authRequest(h, "GET", "/api/notes", "", &http.Cookie{Name: cookieName, Value: testToken}, false)
	if w.Code != 401 || len(w.Result().Cookies()) != 1 {
		t.Fatal("expired session not rejected and cleared")
	}
	repo.sessionErr = errors.New("private session storage details")
	w = authRequest(h, "GET", "/api/notes", "", &http.Cookie{Name: cookieName, Value: testToken}, false)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private session storage details") {
		t.Fatal("storage failure leaked")
	}
}

func TestCSRFAndDisabledRegistration(t *testing.T) {
	repo := &fakeRepository{}
	h := handler(repo)
	for _, origin := range []string{"https://attacker.example", "https://example.com", "null"} {
		r := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"private"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Noters-CSRF", "1")
		r.Header.Set("Origin", origin)
		r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("cross-origin request accepted: %s", origin)
		}
	}
	h = New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{SessionTTL: time.Hour})
	w := authRequest(h, "GET", "/api/auth/config", "", nil, false)
	var config map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	if config["registration_enabled"] {
		t.Fatal("registration config is incorrect")
	}
	w = authRequest(h, "POST", "/api/auth/register", `{"username":"alice","password":"a long password"}`, nil, true)
	if w.Code != 403 {
		t.Fatal("disabled registration accepted")
	}
}

func TestSecureCookieAndSessionRotation(t *testing.T) {
	repo := &fakeRepository{sessions: map[string]storage.User{}}
	h := New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{SessionTTL: time.Hour, SecureCookie: true, AllowRegistration: true})
	first := authRequest(h, "POST", "/api/auth/register", `{"username":"alice","password":"a long password"}`, nil, true).Result().Cookies()[0]
	w := authRequest(h, "POST", "/api/auth/login", `{"username":"alice","password":"a long password"}`, first, true)
	if w.Code != 200 {
		t.Fatal("login failed")
	}
	second := w.Result().Cookies()[0]
	if !second.Secure || !second.HttpOnly || first.Value == second.Value {
		t.Fatal("session not securely rotated")
	}
	if _, exists := repo.sessions[auth.TokenHash(first.Value)]; exists {
		t.Fatal("old session was not revoked")
	}
}

func TestRegistrationValidation(t *testing.T) {
	h := handler(&fakeRepository{})
	for _, body := range []string{
		`{"username":"ab","password":"a long password"}`,
		`{"username":"alice","password":"short"}`,
		`{"username":"Имя","password":"a long password"}`,
		`{"username":"alice","password":"a long password","role":"admin"}`,
		`null`,
	} {
		w := authRequest(h, "POST", "/api/auth/register", body, nil, true)
		if w.Code != 400 {
			t.Fatalf("invalid credentials accepted: %s", body)
		}
	}
}

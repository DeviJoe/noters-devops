package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"noters/internal/auth"
	"strings"
	"testing"
	"time"

	"noters/internal/storage"
)

type fakeRepository struct {
	note          storage.Note
	err           error
	query         string
	limit, offset int
	calls         int
	userID        int64
	users         map[string]storage.User
	sessions      map[string]storage.User
	sessionErr    error
}

func (f *fakeRepository) Ping(context.Context) error { return f.err }
func (f *fakeRepository) List(_ context.Context, userID int64, q string, limit, offset int) (storage.List, error) {
	f.query, f.limit, f.offset, f.userID = q, limit, offset, userID
	return storage.List{Notes: []storage.Note{}, Limit: limit, Offset: offset}, f.err
}
func (f *fakeRepository) Get(context.Context, int64, int64) (storage.Note, error) {
	return f.note, f.err
}
func (f *fakeRepository) Create(_ context.Context, userID int64, input storage.Input) (storage.Note, error) {
	f.calls++
	f.userID = userID
	f.note = storage.Note{ID: 1, Title: input.Title, Content: input.Content, Pinned: input.Pinned}
	return f.note, f.err
}
func (f *fakeRepository) Update(_ context.Context, userID, id int64, input storage.Input) (storage.Note, error) {
	f.calls++
	f.userID = userID
	f.note = storage.Note{ID: id, Title: input.Title, Content: input.Content, Pinned: input.Pinned}
	return f.note, f.err
}
func (f *fakeRepository) Delete(context.Context, int64, int64) error { return f.err }

func handler(repo *fakeRepository) http.Handler {
	return New(repo, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{SessionTTL: 24 * time.Hour, AllowRegistration: true})
}

func request(h http.Handler, method, path, contentType, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	r.Header.Set("X-Noters-CSRF", "1")
	r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestInvalidInputDoesNotReachDatabase(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body string
		status                  int
	}{
		{"missing media type", "", `{"title":"ok"}`, 415},
		{"invalid JSON", "application/json", `{`, 400},
		{"null", "application/json", `null`, 400},
		{"blank title", "application/json", `{"title":"  "}`, 400},
		{"unknown field", "application/json", `{"title":"ok","owner":1}`, 400},
		{"multiple objects", "application/json", `{"title":"ok"}{"title":"other"}`, 400},
		{"long Unicode title", "application/json", `{"title":"` + strings.Repeat("я", 201) + `"}`, 400},
		{"NUL", "application/json", `{"title":"ok","content":"\u0000"}`, 400},
		{"long content", "application/json", `{"title":"ok","content":"` + strings.Repeat("a", 100001) + `"}`, 400},
		{"large payload", "application/json", `{"title":"ok","content":"` + strings.Repeat("a", 1<<20) + `"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepository{}
			w := request(handler(repo), "POST", "/api/notes", tc.contentType, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if repo.calls != 0 {
				t.Fatal("invalid request reached database")
			}
		})
	}
}

func TestCreatePreservesUnicodeAndTrimsTitle(t *testing.T) {
	repo := &fakeRepository{}
	w := request(handler(repo), "POST", "/api/notes", "application/json; charset=utf-8", `{"title":"  Идея 🌱  ","content":"<script>alert(1)</script>\nТекст","pinned":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var note storage.Note
	if err := json.Unmarshal(w.Body.Bytes(), &note); err != nil {
		t.Fatal(err)
	}
	if note.Title != "Идея 🌱" || note.Content != "<script>alert(1)</script>\nТекст" || !note.Pinned {
		t.Fatalf("unexpected note: %#v", note)
	}
	if w.Header().Get("Location") != "/api/notes/1" {
		t.Fatal("missing Location")
	}
}

func TestListValidationAndPagination(t *testing.T) {
	repo := &fakeRepository{}
	h := handler(repo)
	for _, path := range []string{"/api/notes?limit=0", "/api/notes?limit=101", "/api/notes?offset=-1", "/api/notes?offset=no", "/api/notes?q=%00"} {
		if w := request(h, "GET", path, "", ""); w.Code != 400 {
			t.Errorf("%s: status = %d", path, w.Code)
		}
	}
	w := request(h, "GET", "/api/notes?q=hello%25_&limit=7&offset=3", "", "")
	if w.Code != 200 || repo.query != "hello%_" || repo.limit != 7 || repo.offset != 3 {
		t.Fatalf("unexpected pagination: %#v, status %d", repo, w.Code)
	}
	if !strings.Contains(w.Body.String(), `"notes":[]`) {
		t.Fatal("empty list must be an array")
	}
}

func TestErrorsAndHealth(t *testing.T) {
	repo := &fakeRepository{err: storage.ErrNotFound}
	h := handler(repo)
	if w := request(h, "GET", "/api/notes/42", "", ""); w.Code != 404 {
		t.Fatalf("missing note status = %d", w.Code)
	}
	if w := request(h, "DELETE", "/api/notes/-1", "", ""); w.Code != 400 {
		t.Fatalf("invalid ID status = %d", w.Code)
	}
	repo.err = errors.New("private database details")
	w := request(h, "GET", "/api/notes/1", "", "")
	if w.Code != 500 || strings.Contains(w.Body.String(), "private database details") {
		t.Fatal("internal error leaked or wrong status")
	}
	if w := request(h, "GET", "/healthz", "", ""); w.Code != 200 {
		t.Fatal("liveness should not depend on database")
	}
	if w := request(h, "GET", "/readyz", "", ""); w.Code != 503 {
		t.Fatal("readiness must check database")
	}
	repo.err = nil
	w = request(h, "DELETE", "/api/notes/1", "", "")
	if w.Code != 204 || w.Body.Len() != 0 {
		t.Fatal("DELETE should return an empty 204")
	}
}

const testToken = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func (f *fakeRepository) CreateUser(_ context.Context, username, hash string) (storage.User, error) {
	if f.users == nil {
		f.users = map[string]storage.User{}
	}
	if _, exists := f.users[username]; exists {
		return storage.User{}, storage.ErrUsernameTaken
	}
	user := storage.User{ID: int64(len(f.users) + 1), Username: username, PasswordHash: hash}
	f.users[username] = user
	return user, nil
}
func (f *fakeRepository) UserByUsername(_ context.Context, username string) (storage.User, error) {
	user, ok := f.users[username]
	if !ok {
		return user, storage.ErrUserNotFound
	}
	return user, nil
}
func (f *fakeRepository) CreateSession(_ context.Context, userID int64, hash string, _ time.Time) error {
	if f.sessions == nil {
		f.sessions = map[string]storage.User{}
	}
	for _, user := range f.users {
		if user.ID == userID {
			f.sessions[hash] = user
			break
		}
	}
	return nil
}
func (f *fakeRepository) SessionUser(_ context.Context, hash string) (storage.User, error) {
	if f.sessionErr != nil {
		return storage.User{}, f.sessionErr
	}
	if f.sessions == nil && hash == auth.TokenHash(testToken) {
		return storage.User{ID: 7, Username: "tester"}, nil
	}
	user, exists := f.sessions[hash]
	if !exists {
		return user, storage.ErrUnauthenticated
	}
	return user, nil
}
func (f *fakeRepository) DeleteSession(_ context.Context, hash string) error {
	delete(f.sessions, hash)
	return nil
}

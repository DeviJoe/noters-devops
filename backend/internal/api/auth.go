package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"noters/internal/auth"
	"noters/internal/storage"
)

const cookieName = "noters_session"

type userContextKey struct{}

var usernamePattern = regexp.MustCompile(`^[a-z0-9._-]{3,32}$`)

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func currentUser(r *http.Request) storage.User {
	return r.Context().Value(userContextKey{}).(storage.User)
}

func (s *Server) validCSRF(r *http.Request) bool {
	// A custom header requires CORS preflight for cross-origin browser requests.
	// No CORS policy is granted. Origin and Fetch Metadata are also checked.
	if r.Header.Get("X-Noters-CSRF") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil || s.options.SecureCookie {
			scheme = "https"
		}
		if err != nil || parsed.Host != r.Host || parsed.Scheme != scheme || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
			return false
		}
	}
	return true
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.options.SecureCookie, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (s *Server) authenticated(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil || !auth.ValidToken(cookie.Value) {
			writeError(w, http.StatusUnauthorized, "Войдите в учётную запись")
			return
		}
		user, err := s.repo.SessionUser(r.Context(), auth.TokenHash(cookie.Value))
		if errors.Is(err, storage.ErrUnauthenticated) {
			s.clearCookie(w)
			writeError(w, http.StatusUnauthorized, "Сессия истекла. Войдите снова")
			return
		}
		if err != nil {
			s.authError(w, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey{}, user)))
	}
}

func readCredentials(w http.ResponseWriter, r *http.Request, registering bool) (*credentials, bool) {
	input, ok := readJSON[credentials](w, r, 8<<10)
	if !ok {
		return nil, false
	}
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	if !usernamePattern.MatchString(input.Username) {
		writeError(w, http.StatusBadRequest, "Логин: 3–32 символа, латинские буквы, цифры, точка, дефис и подчёркивание")
		return nil, false
	}
	length := utf8.RuneCountInString(input.Password)
	if !utf8.ValidString(input.Password) || strings.ContainsRune(input.Password, 0) || length < 1 || length > 128 || (registering && length < 12) {
		writeError(w, http.StatusBadRequest, "Пароль: от 12 до 128 символов при регистрации")
		return nil, false
	}
	return input, true
}

func (s *Server) authError(w http.ResponseWriter, err error) {
	s.log.Error("authentication storage failed", "error", err)
	writeError(w, http.StatusInternalServerError, "Не удалось выполнить операцию с учётной записью")
}

func (s *Server) acquireHashSlot(w http.ResponseWriter) bool {
	select {
	case s.hashSlots <- struct{}{}:
		return true
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "Слишком много попыток входа. Попробуйте чуть позже")
		return false
	}
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user storage.User, status int) {
	token, err := auth.NewToken()
	if err != nil {
		s.authError(w, err)
		return
	}
	expires := time.Now().UTC().Add(s.options.SessionTTL)
	if err = s.repo.CreateSession(r.Context(), user.ID, auth.TokenHash(token), expires); err != nil {
		s.authError(w, err)
		return
	}
	// Replace any previous session in this browser, including another account's.
	if previous, err := r.Cookie(cookieName); err == nil && auth.ValidToken(previous.Value) {
		if err = s.repo.DeleteSession(r.Context(), auth.TokenHash(previous.Value)); err != nil {
			s.authError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: s.options.SecureCookie, MaxAge: int(s.options.SessionTTL.Seconds()), Expires: expires})
	writeJSON(w, status, map[string]any{"user": user})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.options.AllowRegistration {
		writeError(w, http.StatusForbidden, "Регистрация новых пользователей отключена")
		return
	}
	input, ok := readCredentials(w, r, true)
	if !ok || !s.acquireHashSlot(w) {
		return
	}
	defer func() { <-s.hashSlots }()
	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		s.authError(w, err)
		return
	}
	user, err := s.repo.CreateUser(r.Context(), input.Username, hash)
	if errors.Is(err, storage.ErrUsernameTaken) {
		writeError(w, http.StatusConflict, "Этот логин уже занят")
		return
	}
	if err != nil {
		s.authError(w, err)
		return
	}
	s.startSession(w, r, user, http.StatusCreated)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	input, ok := readCredentials(w, r, false)
	if !ok || !s.acquireHashSlot(w) {
		return
	}
	defer func() { <-s.hashSlots }()
	user, err := s.repo.UserByUsername(r.Context(), input.Username)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		s.authError(w, err)
		return
	}
	hash := user.PasswordHash
	if errors.Is(err, storage.ErrUserNotFound) {
		hash = auth.DummyHash
	}
	valid := auth.VerifyPassword(input.Password, hash)
	if !valid || err != nil {
		writeError(w, http.StatusUnauthorized, "Неверный логин или пароль")
		return
	}
	s.startSession(w, r, user, http.StatusOK)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": currentUser(r)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(cookieName)
	if err := s.repo.DeleteSession(r.Context(), auth.TokenHash(cookie.Value)); err != nil {
		s.authError(w, err)
		return
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"noters/internal/storage"
)

type Repository interface {
	Ping(context.Context) error
	List(context.Context, int64, string, int, int) (storage.List, error)
	Get(context.Context, int64, int64) (storage.Note, error)
	Create(context.Context, int64, storage.Input) (storage.Note, error)
	Update(context.Context, int64, int64, storage.Input) (storage.Note, error)
	Delete(context.Context, int64, int64) error
	CreateUser(context.Context, string, string) (storage.User, error)
	UserByUsername(context.Context, string) (storage.User, error)
	CreateSession(context.Context, int64, string, time.Time) error
	SessionUser(context.Context, string) (storage.User, error)
	DeleteSession(context.Context, string) error
}

type Options struct {
	SessionTTL        time.Duration
	SecureCookie      bool
	AllowRegistration bool
}

type Server struct {
	repo      Repository
	log       *slog.Logger
	options   Options
	hashSlots chan struct{}
}

func New(repo Repository, log *slog.Logger, options Options) http.Handler {
	s := &Server{repo: repo, log: log, options: options, hashSlots: make(chan struct{}, 2)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /api/auth/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"registration_enabled": options.AllowRegistration})
	})
	mux.HandleFunc("POST /api/auth/register", s.register)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/me", s.authenticated(s.me))
	mux.HandleFunc("POST /api/auth/logout", s.authenticated(s.logout))
	mux.HandleFunc("GET /api/notes", s.authenticated(s.list))
	mux.HandleFunc("POST /api/notes", s.authenticated(s.create))
	mux.HandleFunc("GET /api/notes/{id}", s.authenticated(s.get))
	mux.HandleFunc("PUT /api/notes/{id}", s.authenticated(s.update))
	mux.HandleFunc("DELETE /api/notes/{id}", s.authenticated(s.delete))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Маршрут не найден")
	})
	return s.middleware(mux)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		defer func() {
			if err := recover(); err != nil {
				s.log.Error("request panic", "error", err)
				writeError(w, http.StatusInternalServerError, "Внутренняя ошибка сервера")
			}
			if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
				s.log.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
			}
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions && !s.validCSRF(r) {
			writeError(w, http.StatusForbidden, "Недопустимый источник запроса или отсутствует X-Noters-CSRF")
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.repo.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "База данных недоступна")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) result(w http.ResponseWriter, status int, value any, err error) {
	if errors.Is(err, storage.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Заметка не найдена")
		return
	}
	if err != nil {
		s.log.Error("database operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "Не удалось выполнить операцию с заметкой")
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, value)
}

func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "Некорректный ID заметки")
		return 0, false
	}
	return id, true
}

func readJSON[T any](w http.ResponseWriter, r *http.Request, limit int64) (*T, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Ожидается Content-Type: application/json")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	// Decode into a pointer so JSON null cannot be treated as an empty note.
	var payload *T
	if err := decoder.Decode(&payload); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Тело запроса превышает допустимый размер")
		} else {
			writeError(w, http.StatusBadRequest, "Некорректный JSON или неизвестные поля")
		}
		return nil, false
	}
	trailing := decoder.Decode(new(any))
	var tooLarge *http.MaxBytesError
	if errors.As(trailing, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "Тело запроса превышает допустимый размер")
		return nil, false
	}
	if payload == nil || trailing != io.EOF {
		writeError(w, http.StatusBadRequest, "Ожидается один JSON-объект")
		return nil, false
	}
	return payload, true
}

func readInput(w http.ResponseWriter, r *http.Request) (storage.Input, bool) {
	payload, ok := readJSON[storage.Input](w, r, 1<<20)
	if !ok {
		return storage.Input{}, false
	}
	input := *payload
	input.Title = strings.TrimSpace(input.Title)
	if !utf8.ValidString(input.Title) || strings.ContainsRune(input.Title, 0) || utf8.RuneCountInString(input.Title) < 1 || utf8.RuneCountInString(input.Title) > 200 {
		writeError(w, http.StatusBadRequest, "Заголовок должен содержать от 1 до 200 символов без NUL")
		return input, false
	}
	if !utf8.ValidString(input.Content) || strings.ContainsRune(input.Content, 0) || utf8.RuneCountInString(input.Content) > 100000 {
		writeError(w, http.StatusBadRequest, "Текст должен содержать не более 100 000 символов без NUL")
		return input, false
	}
	return input, true
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if !utf8.ValidString(query) || strings.ContainsRune(query, 0) || utf8.RuneCountInString(query) > 200 {
		writeError(w, http.StatusBadRequest, "Поисковый запрос не должен превышать 200 символов и содержать NUL")
		return
	}
	limit, offset := 50, 0
	for _, param := range []struct {
		name     string
		value    *int
		min, max int
	}{{"limit", &limit, 1, 100}, {"offset", &offset, 0, 1000000}} {
		if value, present := r.URL.Query()[param.name]; present {
			n, err := strconv.Atoi(value[0])
			if err != nil || n < param.min || n > param.max {
				writeError(w, http.StatusBadRequest, "Некорректный параметр "+param.name)
				return
			}
			*param.value = n
		}
	}
	value, err := s.repo.List(r.Context(), currentUser(r).ID, query, limit, offset)
	s.result(w, http.StatusOK, value, err)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	value, err := s.repo.Get(r.Context(), currentUser(r).ID, id)
	s.result(w, http.StatusOK, value, err)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	input, ok := readInput(w, r)
	if !ok {
		return
	}
	value, err := s.repo.Create(r.Context(), currentUser(r).ID, input)
	if err == nil {
		w.Header().Set("Location", "/api/notes/"+strconv.FormatInt(value.ID, 10))
	}
	s.result(w, http.StatusCreated, value, err)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	input, ok := readInput(w, r)
	if !ok {
		return
	}
	value, err := s.repo.Update(r.Context(), currentUser(r).ID, id, input)
	s.result(w, http.StatusOK, value, err)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	s.result(w, http.StatusNoContent, nil, s.repo.Delete(r.Context(), currentUser(r).ID, id))
}

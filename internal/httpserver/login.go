package httpserver

import (
	"context"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"

	"kids-music-player/internal/auth"
)

type userKey struct{}

func withUser(ctx context.Context, user auth.User) context.Context {
	return context.WithValue(ctx, userKey{}, user)
}

var loginPage = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="ru">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
  <meta name="theme-color" content="#fff4e6">
  <title>Вход — Музыка</title>
  <link rel="icon" href="/favicon-32.png" type="image/png" sizes="32x32">
  <link rel="stylesheet" href="/app.css">
  <style>
    body { padding-bottom: 0; }
    .login { max-width: 22rem; margin: 4rem auto; padding: 0 1rem; }
    .login h1 { margin: 0 0 2rem; font-size: 1.75rem; }
    .login label { display: block; margin: 0 0 1.25rem; }
    .login span { display: block; margin: 0 0 0.4rem; }
    .login input {
      display: block; box-sizing: border-box; width: 100%;
      min-height: 3rem; margin: 0; padding: 0.6rem 0.9rem;
      border: 1px solid var(--line); border-radius: 1rem; background: var(--card);
    }
    .login button {
      display: block; width: 100%; margin-top: 0.5rem;
      background: var(--peach); color: #fff;
    }
    .login .err { margin: 0 0 1rem; }
  </style>
</head>
<body>
  <main class="login">
    <h1>Музыка</h1>
    <form method="post" action="/login">
      <label>
        <span>Имя</span>
        <input name="name" autocomplete="username" required autofocus>
      </label>
      <label>
        <span>Пароль</span>
        <input name="password" type="password" autocomplete="current-password" required>
      </label>
      {{if .Error}}<p class="err notice" role="alert">{{.Error}}</p>{{end}}
      <button type="submit">Войти</button>
    </form>
  </main>
</body>
</html>`))

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if _, err := s.userFromRequest(r); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		s.renderLogin(w, http.StatusOK, "")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderLogin(w, http.StatusBadRequest, "некорректная форма")
		return
	}
	token, err := s.accounts.Login(r.Form.Get("name"), r.Form.Get("password"))
	if err != nil {
		msg := "неверное имя или пароль"
		if errors.Is(err, auth.ErrDisabled) {
			msg = "учётная запись отключена"
		} else if !errors.Is(err, auth.ErrCredentials) {
			s.logger.Error("login", slog.Any("err", err))
			msg = "не удалось войти"
		}
		s.renderLogin(w, http.StatusUnauthorized, msg)
		return
	}
	http.SetCookie(w, sessionCookie(token, r.TLS != nil))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		if err := s.accounts.Logout(c.Value); err != nil {
			s.logger.Error("logout", slog.Any("err", err))
		}
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	name := ""
	if u, err := s.userFromRequest(r); err == nil {
		name = u.Name
	}
	s.respondJSON(w, http.StatusOK, map[string]string{"name": name})
}

func (s *Server) renderLogin(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := loginPage.Execute(w, map[string]string{"Error": msg}); err != nil {
		s.logger.Error("login page", slog.Any("err", err))
	}
}

func sessionCookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   auth.SessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	}
}

func (s *Server) userFromRequest(r *http.Request) (auth.User, error) {
	if s.accounts == nil {
		return auth.User{}, auth.ErrCredentials
	}
	c, err := r.Cookie(auth.CookieName)
	if err != nil {
		return auth.User{}, auth.ErrCredentials
	}
	return s.accounts.UserByToken(c.Value)
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authOn || isPublic(r) {
			next.ServeHTTP(w, r)
			return
		}
		user, err := s.userFromRequest(r)
		if err != nil {
			if errors.Is(err, auth.ErrDisabled) {
				http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
			} else if !errors.Is(err, auth.ErrCredentials) {
				s.logger.Error("session", slog.Any("err", err))
			}
			if wantsPage(r) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			s.respondError(w, http.StatusUnauthorized, "нужен вход")
			return
		}
		next.ServeHTTP(w, r.WithContext(withUser(r.Context(), user)))
	})
}

func isPublic(r *http.Request) bool {
	switch r.URL.Path {
	case "/login", "/healthz", "/app.css", "/app.js", "/alpine.min.js",
		"/manifest.webmanifest", "/icon-192.png", "/icon-512.png",
		"/apple-touch-icon.png", "/favicon-32.png":
		return true
	default:
		return false
	}
}

func wantsPage(r *http.Request) bool {
	return r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/")
}

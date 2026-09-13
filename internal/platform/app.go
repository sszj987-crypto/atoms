package platform

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type App struct {
	cfg      Config
	db       *pgxpool.Pool
	auth     *authService
	model    *modelService
	projects *projectService
	chat     *chatService
}

func NewApp(ctx context.Context, cfg Config) (*App, error) {
	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	// Backfill per-user deploy ports for accounts created before this feature.
	if _, err := db.Exec(ctx, `UPDATE users SET deploy_port = $1 + (nextval('user_port_seq') - 1) * $2 WHERE deploy_port IS NULL`, cfg.DeployPortBase, cfg.DeployPortSpan); err != nil {
		db.Close()
		return nil, err
	}
	// Each user owns a reserved port range. Keep each project's port stable so
	// multiple project runtimes can run without binding the same host port.
	if _, err := db.Exec(ctx, `
		WITH ranked AS (
			SELECT p.id, u.deploy_port + (row_number() OVER (PARTITION BY p.user_id ORDER BY p.created_at, p.id) - 1)::integer AS deploy_port
			FROM projects p
			JOIN users u ON u.id = p.user_id
		)
		UPDATE projects p
		SET deploy_port = ranked.deploy_port
		FROM ranked
		WHERE p.id = ranked.id AND p.deploy_port IS NULL
	`); err != nil {
		db.Close()
		return nil, err
	}
	model := newModelService(db, cfg.MasterKey)
	projects := newProjectService(db, cfg, model)
	projects.recover(ctx)
	projects.startCleanup(context.Background())
	return &App{cfg: cfg, db: db, auth: newAuthService(db, cfg.SessionKey, cfg.DeployPortBase, cfg.DeployPortSpan), model: model, projects: projects, chat: newChatService(projects)}, nil
}

func (a *App) Close() { a.db.Close() }

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if projectID := previewProjectID(r.Host); projectID != "" {
			token := r.URL.Query().Get("preview_token")
			fromQuery := token != ""
			if !fromQuery {
				if cookie, err := r.Cookie(previewCookie); err == nil {
					token = cookie.Value
				}
			}
			if userID, tokenProjectID, ok := a.auth.readPreview(token); ok && tokenProjectID == projectID {
				var u User
				if err := a.db.QueryRow(r.Context(), `SELECT id,email FROM users WHERE id=$1`, userID).Scan(&u.ID, &u.Email); err == nil {
					if fromQuery {
						a.auth.setPreviewCookie(w, token)
					}
					a.preview(w, contextWithUser(r, u))
					return
				}
			}
			a.auth.requireUser(http.HandlerFunc(a.preview)).ServeHTTP(w, r)
			return
		}
		a.platformHandler().ServeHTTP(w, r)
	}))
	return r
}

func (a *App) platformHandler() http.Handler {
	r := chi.NewRouter()
	r.Get("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/register", a.auth.register)
		r.Post("/auth/login", a.auth.login)
		r.Post("/auth/logout", a.auth.logout)
		r.Group(func(r chi.Router) {
			r.Use(a.auth.requireUser, middleware.Timeout(30*time.Second))
			r.Get("/me", a.auth.me)
			r.Get("/llm-config", a.model.get)
			r.Put("/llm-config", a.model.put)
			r.Post("/llm-config/test", a.model.test)
			r.Get("/project", a.projects.get)
			r.Post("/project", a.projects.create)
			r.Patch("/project/{id}", a.projects.rename)
			r.Delete("/project/{id}", a.projects.delete)
			r.Get("/project/{id}/preview-access", a.previewAccess)
			r.Get("/project/{id}/runtime/status", a.projects.runtimeStatus)
			r.Post("/project/{id}/runtime/restart", a.projects.restart)
			r.Post("/project/{id}/deploy", a.projects.deploy)
			r.Get("/project/{id}/messages", a.chat.messages)
			r.Post("/project/{id}/messages", a.chat.send)
			r.Get("/project/runs/{id}", a.chat.runInfo)
			r.Post("/project/runs/{id}/cancel", a.chat.cancel)
		})
		// SSE is long-lived (agent runs take minutes); exclude it from the request timeout.
		r.With(a.auth.requireUser).Get("/project/runs/{id}/events", a.chat.events)
	})
	if web, err := staticHandler(a.cfg.WebDir); err == nil {
		r.Mount("/", web)
	} else {
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "atoms platform API ready"})
		})
	}
	return r
}

func (a *App) previewAccess(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(currentUserKey{}).(User)
	p, err := a.projects.byID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_READ_FAILED")
		return
	}
	if err := a.projects.ensureRuntime(r.Context(), p); err != nil {
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	if err := waitRuntimeReady(r.Context(), p.ID); err != nil {
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"preview_token": a.auth.signPreview(u.ID, p.ID), "port": p.DeployPort})
}

func (a *App) preview(w http.ResponseWriter, r *http.Request) {
	projectID := previewProjectID(r.Host)
	u := r.Context().Value(currentUserKey{}).(User)
	p, err := a.projects.byID(r.Context(), u.ID, projectID)
	if err != nil || p.ID != projectID {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	if err := a.projects.ensureRuntime(r.Context(), p); err != nil {
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	a.projects.touch(r.Context(), p.ID)
	target, _ := url.Parse("http://" + runtimeName(p.ID) + ":3000")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		// Runtime is still booting (pnpm install + next dev). Return a page that
		// reloads itself so the iframe recovers without the user hitting Refresh.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<meta http-equiv="refresh" content="2"><body style="font-family:system-ui,sans-serif;color:#61708a;padding:2rem">Starting preview…</body>`))
	}
	proxy.ServeHTTP(w, r)
}
func previewProjectID(host string) string {
	host = strings.Split(host, ":")[0]
	if !strings.HasPrefix(host, "p-") || !strings.HasSuffix(host, ".localhost") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(host, "p-"), ".localhost")
	if len(id) != 36 {
		return ""
	}
	return id
}

func staticHandler(dir string) (http.Handler, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, errors.New("frontend bundle unavailable")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(filepath.Clean(r.URL.Path), "/")
		if name == "." || name == "" || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			name = "index.html"
		}
		target := filepath.Join(dir, name)
		fileInfo, err := os.Stat(target)
		if err != nil || fileInfo.IsDir() {
			name, target = "index.html", filepath.Join(dir, "index.html")
			fileInfo, err = os.Stat(target)
			if err != nil {
				http.NotFound(w, r)
				return
			}
		}
		file, err := os.Open(target)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()
		http.ServeContent(w, r, name, fileInfo.ModTime(), file)
	}), nil
}

func migrate(ctx context.Context, db *pgxpool.Pool) error {
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	for _, name := range entries {
		raw, err := migrationFiles.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := db.Exec(ctx, string(raw)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

func decodeJSON(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 64<<10)
	de := json.NewDecoder(r.Body)
	de.DisallowUnknownFields()
	return de.Decode(dst)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func apiError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

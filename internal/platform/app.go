package platform

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type App struct {
	cfg         Config
	db          *pgxpool.Pool
	auth        *authService
	model       *modelService
	projects    *projectService
	chat        *chatService
	cancel      context.CancelFunc
	shutdown    sync.Once
	stopErr     error
	close       sync.Once
	gatewayOnce sync.Once
	gateway     *previewGateway
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
	chat := newChatService(projects)
	if err := projects.recover(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover project runtimes: %w", err)
	}
	if err := chat.recoverInterruptedRuns(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover interrupted runs: %w", err)
	}
	if err := projects.recoverRestores(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("recover project restores: %w", err)
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	projects.startCleanup(lifecycleCtx)
	return &App{cfg: cfg, db: db, auth: newAuthService(db, cfg.SessionKey, cfg.DeployPortBase, cfg.DeployPortSpan), model: model, projects: projects, chat: chat, cancel: lifecycleCancel}, nil
}

func (a *App) Close() {
	a.close.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "atoms shutdown cleanup: %v\n", err)
		}
		a.db.Close()
	})
}

func (a *App) Shutdown(ctx context.Context) error {
	a.shutdown.Do(func() {
		a.cancel()
		a.stopErr = a.projects.shutdownRestores(ctx)
		if err := a.chat.shutdown(ctx); a.stopErr == nil {
			a.stopErr = err
		}
	})
	return a.stopErr
}

func hsts(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if secureRequest(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, hsts, middleware.Recoverer)
	r.Handle(previewNamespace+"*", http.HandlerFunc(a.servePreview))
	r.Mount("/", a.platformHandler())
	return r
}

func (a *App) servePreview(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, previewNamespace), "/", 3)
	if len(parts) < 2 || !validID(parts[0]) {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	projectID := parts[0]
	prefix := previewPath(a.cfg.MasterKey, projectID)
	if r.URL.Path != prefix && !strings.HasPrefix(r.URL.Path, prefix+"/") {
		apiError(w, 401, "AUTH_REQUIRED")
		return
	}
	// Never render a preview as a standalone website, even with old cookies.
	if r.Header.Get("Sec-Fetch-Dest") == "document" {
		apiError(w, 403, "PREVIEW_EMBED_ONLY")
		return
	}
	userID := a.previewGateway().owner(projectID)
	if userID == "" {
		apiError(w, 401, "AUTH_REQUIRED")
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "null" && origin != requestOrigin(r) {
		apiError(w, 403, "PREVIEW_ORIGIN_DENIED")
		return
	}
	p, err := a.projects.byID(r.Context(), userID, projectID)
	if err != nil {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Access-Control-Allow-Origin", "null")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Add("Vary", "Origin")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.projects.ensureRuntime(r.Context(), p); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	port := "3000"
	if p.Deployed {
		port = "3001"
	}
	target, _ := url.Parse("http://" + runtimeName(p.ID) + ":" + port)
	proxy := newPreviewProxy(target)
	restrictPreviewProxy(proxy, prefix, requestOrigin(r), "/projects/"+projectID)
	proxy.ServeHTTP(w, r)
}

func (a *App) platformHandler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Different ports are different origins, but share host cookies. Keep
			// generated app JavaScript from using those cookies against the API.
			if strings.HasPrefix(r.URL.Path, "/api/") && !sameOriginRequest(r) {
				apiError(w, http.StatusForbidden, "ORIGIN_DENIED")
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	r.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		components := map[string]string{"postgres": "ok", "docker": "ok"}
		status := http.StatusOK
		if err := a.db.Ping(ctx); err != nil {
			components["postgres"] = "unavailable"
			status = http.StatusServiceUnavailable
		}
		if err := a.projects.docker.ping(ctx); err != nil {
			components["docker"] = "unavailable"
			status = http.StatusServiceUnavailable
		}
		overall := "ok"
		if status != http.StatusOK {
			overall = "unavailable"
		}
		writeJSON(w, status, map[string]any{"status": overall, "components": components})
	})
	r.Route("/api", func(r chi.Router) {
		r.With(authRateLimit).Post("/auth/register", a.auth.register)
		r.With(authRateLimit).Post("/auth/login", a.auth.login)
		r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
			if cookie, err := r.Cookie(sessionCookie); err == nil {
				if userID, ok := a.auth.readSession(cookie.Value); ok {
					a.previewGateway().revoke(userID)
				}
			}
			a.auth.logout(w, r)
		})
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
			r.Get("/project/{id}/files", a.projects.files)
			r.Get("/project/{id}/versions", a.projects.versions)
			r.Get("/project/{id}/versions/{versionID}/thumbnail", a.projects.versionThumbnail)
			r.Post("/project/{id}/restore", a.projects.startRestore)
			r.Get("/project/{id}/restore/{operationID}", a.projects.getRestore)
			r.Get("/project/{id}/file", a.projects.file)
			r.Get("/project/{id}/file/download", a.projects.downloadFile)
			r.Get("/project/{id}/export", a.projects.exportSource)
			r.Get("/project/{id}/runtime/status", a.projects.runtimeStatus)
			r.Post("/project/{id}/runtime/restart", a.projects.restart)
			r.Get("/project/{id}/messages", a.chat.messages)
			r.Post("/project/{id}/messages", a.chat.send)
			r.Get("/project/{id}/runs/active", a.chat.activeRun)
			r.Get("/project/runs/{id}", a.chat.runInfo)
			r.Post("/project/runs/{id}/cancel", a.chat.cancel)
		})
		// preview-access waits for the runtime to boot (cold start can exceed 30s); exclude it from the timeout.
		r.With(a.auth.requireUser).Get("/project/{id}/preview-access", a.previewAccess)
		r.With(a.auth.requireUser).Post("/project/{id}/deploy", a.projects.deploy)
		r.With(a.auth.requireUser).Post("/project/{id}/undeploy", a.projects.undeploy)
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
		log.Printf("preview-access ensureRuntime failed project=%s deployPort=%d err=%v", p.ID, p.DeployPort, err)
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	if err := waitRuntimeReady(r.Context(), p.ID); err != nil {
		log.Printf("preview-access waitRuntimeReady failed project=%s deployPort=%d err=%v", p.ID, p.DeployPort, err)
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	log.Printf("preview-access ok project=%s deployPort=%d", p.ID, p.DeployPort)
	w.Header().Set("Cache-Control", "no-store")
	a.previewGateway().lease(p.ID, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"url": requestOrigin(r) + previewPath(a.cfg.MasterKey, p.ID) + "/"})
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
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	entries, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	for _, name := range entries {
		tx, err := db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		var applied bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&applied); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if applied {
			tx.Rollback(ctx)
			continue
		}
		raw, err := migrationFiles.ReadFile(name)
		if err != nil {
			tx.Rollback(ctx)
			return err
		}
		if _, err := tx.Exec(ctx, string(raw)); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
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

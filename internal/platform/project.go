package platform

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed starter
var starterFiles embed.FS

type Project struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	WorkspacePath        string    `json:"-"`
	CodexStatePath       string    `json:"-"`
	DBSchema             string    `json:"-"`
	DBUsername           string    `json:"-"`
	DBPasswordCiphertext []byte    `json:"-"`
	LastAccessedAt       time.Time `json:"last_accessed_at"`
	DeployPort           int       `json:"-"`
}
type projectService struct {
	db     *pgxpool.Pool
	cfg    Config
	docker *dockerClient
	model  *modelService
}

func newProjectService(db *pgxpool.Pool, cfg Config, model *modelService) *projectService {
	return &projectService{db: db, cfg: cfg, docker: newDockerClient(), model: model}
}

func (s *projectService) get(w http.ResponseWriter, r *http.Request) {
	p, err := s.current(r.Context(), r.Context().Value(currentUserKey{}).(User).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, 200, map[string]any{"project": nil})
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	s.touch(r.Context(), p.ID)
	writeJSON(w, 200, map[string]any{"project": p})
}
func (s *projectService) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Description string `json:"description"`
	}
	if decodeJSON(r, &input) != nil || strings.TrimSpace(input.Description) == "" {
		apiError(w, 400, "INVALID_PROJECT")
		return
	}
	u := r.Context().Value(currentUserKey{}).(User)
	name, err := s.model.projectTitle(r.Context(), u.ID, input.Description)
	if errors.Is(err, errModelConfigRequired) {
		apiError(w, 400, "MODEL_CONFIG_REQUIRED")
		return
	}
	if err != nil {
		apiError(w, 422, "MODEL_TITLE_UNAVAILABLE")
		return
	}
	p, err := s.createProject(r.Context(), u.ID, name)
	if errors.Is(err, errProjectExists) {
		apiError(w, 409, "PROJECT_LIMIT_REACHED")
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_CREATE_FAILED")
		return
	}
	writeJSON(w, 201, map[string]any{"project": p})
}
func (s *projectService) rename(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name string `json:"name"`
	}
	if decodeJSON(r, &input) != nil {
		apiError(w, 400, "INVALID_PROJECT_NAME")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 32 {
		apiError(w, 400, "INVALID_PROJECT_NAME")
		return
	}
	u := r.Context().Value(currentUserKey{}).(User)
	p, err := s.current(r.Context(), u.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_READ_FAILED")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE projects SET name=$1 WHERE id=$2 AND user_id=$3`, name, p.ID, u.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_RENAME_FAILED")
		return
	}
	p.Name = name
	writeJSON(w, http.StatusOK, map[string]any{"project": p})
}
func (s *projectService) delete(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(currentUserKey{}).(User)
	p, err := s.current(r.Context(), u.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_READ_FAILED")
		return
	}
	var active bool
	if err := s.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE project_id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING'))`, p.ID).Scan(&active); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if active {
		apiError(w, http.StatusConflict, "RUN_IN_PROGRESS")
		return
	}
	if err := s.docker.remove(r.Context(), runtimeName(p.ID)); err != nil {
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	if _, err := s.db.Exec(r.Context(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdent(p.DBSchema))); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if _, err := s.db.Exec(r.Context(), fmt.Sprintf("DROP ROLE IF EXISTS %s", quoteIdent(p.DBUsername))); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM projects WHERE id=$1 AND user_id=$2`, p.ID, u.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	root := filepath.Clean(filepath.Dir(p.WorkspacePath))
	expectedPrefix := filepath.Join(filepath.Clean(s.cfg.ProjectRoot), u.ID, "projects") + string(filepath.Separator)
	if strings.HasPrefix(root+string(filepath.Separator), expectedPrefix) {
		if err := os.RemoveAll(root); err != nil {
			apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *projectService) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	p, err := s.current(r.Context(), r.Context().Value(currentUserKey{}).(User).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	status, err := s.docker.inspect(r.Context(), runtimeName(p.ID))
	if err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	s.touch(r.Context(), p.ID)
	writeJSON(w, 200, map[string]any{"exists": status.Exists, "running": status.Running})
}
func (s *projectService) restart(w http.ResponseWriter, r *http.Request) {
	p, err := s.current(r.Context(), r.Context().Value(currentUserKey{}).(User).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	if err = s.docker.remove(r.Context(), runtimeName(p.ID)); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	if err = s.ensureRuntime(r.Context(), p); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "starting"})
}
func (s *projectService) deploy(w http.ResponseWriter, r *http.Request) {
	p, err := s.current(r.Context(), r.Context().Value(currentUserKey{}).(User).ID)
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	// Recreate so the container is always created with the published deploy port,
	// even if a pre-deploy runtime (no port) is already running.
	if err := s.docker.remove(r.Context(), runtimeName(p.ID)); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	if err := s.ensureRuntime(r.Context(), p); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	writeJSON(w, 200, map[string]string{"port": strconv.Itoa(p.DeployPort)})
}

var errProjectExists = errors.New("one project per user")

func (s *projectService) createProject(ctx context.Context, userID, name string) (Project, error) {
	if _, err := s.current(ctx, userID); err == nil {
		return Project{}, errProjectExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Project{}, err
	}
	id := newID()
	short := strings.ReplaceAll(id, "-", "")[:12]
	schema := "p_" + short
	role := "p_" + short
	password, err := randomSecret()
	if err != nil {
		return Project{}, err
	}
	root := filepath.Join(s.cfg.ProjectRoot, userID, "projects", id)
	p := Project{ID: id, Name: strings.TrimSpace(name), WorkspacePath: filepath.Join(root, "workspace"), CodexStatePath: filepath.Join(root, "codex"), DBSchema: schema, DBUsername: role, LastAccessedAt: time.Now().UTC()}
	if p.Name == "" {
		p.Name = "Untitled project"
	}
	if err := makeRuntimeDirectory(p.WorkspacePath); err != nil {
		return Project{}, err
	}
	if err := makeRuntimeDirectory(p.CodexStatePath); err != nil {
		return Project{}, err
	}
	if err := makeRuntimeDirectory(filepath.Join(root, "logs")); err != nil {
		return Project{}, err
	}
	if err := copyStarter(p.WorkspacePath); err != nil {
		return Project{}, err
	}
	sealed, err := encrypt(s.cfg.MasterKey, password)
	if err != nil {
		return Project{}, err
	}
	p.DBPasswordCiphertext = sealed
	if err := s.createProjectDatabase(ctx, schema, role, password); err != nil {
		return Project{}, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.ID, userID, p.Name, p.WorkspacePath, p.CodexStatePath, p.LastAccessedAt, p.DBSchema, p.DBUsername, p.DBPasswordCiphertext)
	if err != nil {
		return Project{}, err
	}
	return p, nil
}
func (s *projectService) createProjectDatabase(ctx context.Context, schema, role, password string) error {
	_, err := s.db.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s", quoteIdent(role), quoteLiteral(password)))
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s AUTHORIZATION %s", quoteIdent(schema), quoteIdent(role)))
	return err
}
func quoteIdent(v string) string   { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func quoteLiteral(v string) string { return `'` + strings.ReplaceAll(v, `'`, `''`) + `'` }
func (s *projectService) current(ctx context.Context, userID string) (Project, error) {
	var p Project
	err := s.db.QueryRow(ctx, `SELECT p.id,p.name,p.workspace_path,p.codex_state_path,p.db_schema,p.db_username,p.db_password_ciphertext,p.last_accessed_at,COALESCE(u.deploy_port,0) FROM projects p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1`, userID).Scan(&p.ID, &p.Name, &p.WorkspacePath, &p.CodexStatePath, &p.DBSchema, &p.DBUsername, &p.DBPasswordCiphertext, &p.LastAccessedAt, &p.DeployPort)
	return p, err
}
func (s *projectService) ensureRuntime(ctx context.Context, p Project) error {
	status, err := s.docker.inspect(ctx, runtimeName(p.ID))
	if err != nil {
		return err
	}
	if status.Running {
		return nil
	}
	if status.Exists {
		if err := s.docker.remove(ctx, runtimeName(p.ID)); err != nil {
			return err
		}
	}
	password, err := decrypt(s.cfg.MasterKey, p.DBPasswordCiphertext)
	if err != nil {
		return err
	}
	databaseURL := fmt.Sprintf("postgres://%s:%s@postgres:5432/atoms?sslmode=disable&search_path=%s", url.QueryEscape(p.DBUsername), url.QueryEscape(password), url.QueryEscape(p.DBSchema))
	if err = s.docker.createAndStart(ctx, p, databaseURL, s.cfg.RuntimeImage, s.cfg.DataVolumeName, s.cfg.RuntimeNetwork, strconv.Itoa(p.DeployPort), s.cfg.RuntimeCPU, s.cfg.RuntimeMemoryBytes, s.cfg.RuntimePIDs); err != nil {
		return err
	}
	s.touch(ctx, p.ID)
	return nil
}
func (s *projectService) touch(ctx context.Context, id string) {
	_, _ = s.db.Exec(ctx, `UPDATE projects SET last_accessed_at=now() WHERE id=$1 AND last_accessed_at < now() - interval '5 minutes'`, id)
}
func (s *projectService) cleanup(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT id FROM projects WHERE last_accessed_at < now() - $1::interval AND NOT EXISTS (SELECT 1 FROM agent_runs WHERE agent_runs.project_id=projects.id AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING'))`, durationInterval(s.cfg.RuntimeIdleTTL))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if err := s.docker.remove(ctx, runtimeName(id)); err != nil {
			return err
		}
	}
	return rows.Err()
}
func (s *projectService) startCleanup(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.RuntimeSweepInterval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.cleanup(ctx)
			}
		}
	}()
}
func (s *projectService) recover(ctx context.Context) {
	ids, err := s.docker.managedProjectIDs(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		var exists bool
		if s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1)`, id).Scan(&exists) == nil && !exists {
			_ = s.docker.remove(ctx, runtimeName(id))
		}
	}
}
func durationInterval(v time.Duration) string { return fmt.Sprintf("%d seconds", int64(v.Seconds())) }
func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func makeRuntimeDirectory(path string) error {
	if err := os.MkdirAll(path, 0750); err != nil {
		return err
	}
	// The project Runtime runs as uid 1000. The control-plane container is root only so it can prepare this bind mount.
	if os.Geteuid() != 0 {
		return nil
	}
	return os.Chown(path, 1000, 1000)
}
func copyStarter(destination string) error {
	return fs.WalkDir(starterFiles, "starter", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative := strings.TrimPrefix(path, "starter/")
		if entry.IsDir() {
			if relative == "starter" {
				return nil
			}
			target := filepath.Join(destination, relative)
			if err := os.MkdirAll(target, 0750); err != nil {
				return err
			}
			if os.Geteuid() == 0 {
				return os.Chown(target, 1000, 1000)
			}
			return nil
		}
		target := filepath.Join(destination, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return err
		}
		if os.Geteuid() == 0 {
			// Directories must be writable by the non-root Runtime, not merely the files.
			if err := os.Chown(filepath.Dir(target), 1000, 1000); err != nil {
				return err
			}
		}
		raw, err := starterFiles.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target, raw, 0640); err != nil {
			return err
		}
		if os.Geteuid() != 0 {
			return nil
		}
		return os.Chown(target, 1000, 1000)
	})
}

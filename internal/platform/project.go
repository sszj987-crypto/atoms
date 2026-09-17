package platform

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
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
	db           *pgxpool.Pool
	cfg          Config
	docker       *dockerClient
	model        *modelService
	mu           sync.Mutex
	runtimeLocks map[string]*sync.Mutex
}

func newProjectService(db *pgxpool.Pool, cfg Config, model *modelService) *projectService {
	return &projectService{db: db, cfg: cfg, docker: newDockerClient(), model: model, runtimeLocks: make(map[string]*sync.Mutex)}
}

func (s *projectService) get(w http.ResponseWriter, r *http.Request) {
	projects, err := s.list(r.Context(), r.Context().Value(currentUserKey{}).(User).ID)
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	if projects == nil {
		projects = []Project{}
	}
	writeJSON(w, 200, map[string]any{"projects": projects})
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
	allowed, err := s.canCreateProject(r.Context(), u.ID)
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	if !allowed {
		apiError(w, 409, "PROJECT_LIMIT_REACHED")
		return
	}
	name, err := s.model.projectTitle(r.Context(), u.ID, input.Description)
	if errors.Is(err, errModelConfigRequired) {
		apiError(w, 400, "MODEL_CONFIG_REQUIRED")
		return
	}
	if err != nil {
		log.Printf("project create: title unavailable; using default name")
		name = defaultProjectName
	}
	if !validProjectName(name) {
		name = defaultProjectName
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
	if !validProjectName(name) {
		apiError(w, 400, "INVALID_PROJECT_NAME")
		return
	}
	u := r.Context().Value(currentUserKey{}).(User)
	p, err := s.byID(r.Context(), u.ID, chi.URLParam(r, "id"))
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
	p, err := s.byID(r.Context(), u.ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_READ_FAILED")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, p.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	var active bool
	if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE project_id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING'))`, p.ID).Scan(&active); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if active {
		apiError(w, http.StatusConflict, "RUN_IN_PROGRESS")
		return
	}
	if err := s.removeRuntime(r.Context(), p.ID); err != nil {
		apiError(w, http.StatusServiceUnavailable, "RUNTIME_UNAVAILABLE")
		return
	}
	if _, err := tx.Exec(r.Context(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", quoteIdent(p.DBSchema))); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if _, err := tx.Exec(r.Context(), fmt.Sprintf("DROP ROLE IF EXISTS %s", quoteIdent(p.DBUsername))); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM projects WHERE id=$1 AND user_id=$2`, p.ID, u.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_DELETE_FAILED")
		return
	}
	root := filepath.Clean(filepath.Dir(p.WorkspacePath))
	expectedPrefix := filepath.Join(filepath.Clean(s.cfg.ProjectRoot), u.ID, "projects") + string(filepath.Separator)
	if strings.HasPrefix(root+string(filepath.Separator), expectedPrefix) {
		if err := os.RemoveAll(root); err != nil {
			log.Printf("project cleanup failed project_id=%s: %v", p.ID, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *projectService) runtimeStatus(w http.ResponseWriter, r *http.Request) {
	p, err := s.byID(r.Context(), r.Context().Value(currentUserKey{}).(User).ID, chi.URLParam(r, "id"))
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
	p, err := s.byID(r.Context(), r.Context().Value(currentUserKey{}).(User).ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	active, err := s.hasActiveRun(r.Context(), p.ID)
	if err != nil {
		apiError(w, 500, "RUN_READ_FAILED")
		return
	}
	if active {
		apiError(w, 409, "RUN_IN_PROGRESS")
		return
	}
	if err = s.recreateRuntime(r.Context(), p); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "starting"})
}
func (s *projectService) deploy(w http.ResponseWriter, r *http.Request) {
	p, err := s.byID(r.Context(), r.Context().Value(currentUserKey{}).(User).ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return
	}
	active, err := s.hasActiveRun(r.Context(), p.ID)
	if err != nil {
		apiError(w, 500, "RUN_READ_FAILED")
		return
	}
	if active {
		apiError(w, 409, "RUN_IN_PROGRESS")
		return
	}
	if err := s.recreateRuntime(r.Context(), p); err != nil {
		apiError(w, 503, "RUNTIME_UNAVAILABLE")
		return
	}
	writeJSON(w, 200, map[string]string{"port": strconv.Itoa(p.DeployPort)})
}

const maxProjectsPerUser = 2

var errProjectExists = errors.New("project limit reached")

func (s *projectService) canCreateProject(ctx context.Context, userID string) (bool, error) {
	var count int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM projects WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return false, err
	}
	return count < maxProjectsPerUser, nil
}

func (s *projectService) createProject(ctx context.Context, userID, name string) (Project, error) {
	id := newID()
	short := strings.ReplaceAll(id, "-", "")[:12]
	schema := "p_" + short
	role := "p_" + short
	password, err := randomSecret()
	if err != nil {
		return Project{}, err
	}
	root := filepath.Join(s.cfg.ProjectRoot, userID, "projects", id)
	created := false
	defer func() {
		if !created {
			_ = os.RemoveAll(root)
		}
	}()
	p := Project{ID: id, Name: strings.TrimSpace(name), WorkspacePath: filepath.Join(root, "workspace"), CodexStatePath: filepath.Join(root, "codex"), DBSchema: schema, DBUsername: role, LastAccessedAt: time.Now().UTC()}
	if p.Name == "" {
		p.Name = defaultProjectName
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
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, userID); err != nil {
		return Project{}, err
	}
	var count, portBase int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM projects WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return Project{}, err
	}
	if count >= maxProjectsPerUser {
		return Project{}, errProjectExists
	}
	if err := tx.QueryRow(ctx, `SELECT deploy_port FROM users WHERE id=$1`, userID).Scan(&portBase); err != nil {
		return Project{}, err
	}
	rows, err := tx.Query(ctx, `SELECT deploy_port FROM projects WHERE user_id=$1 AND deploy_port IS NOT NULL`, userID)
	if err != nil {
		return Project{}, err
	}
	usedPorts := make([]int, 0, count)
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			rows.Close()
			return Project{}, err
		}
		usedPorts = append(usedPorts, port)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Project{}, err
	}
	rows.Close()
	var ok bool
	p.DeployPort, ok = availableProjectPort(portBase, s.cfg.DeployPortSpan, usedPorts)
	if !ok {
		return Project{}, errProjectExists
	}
	if err := s.createProjectDatabase(ctx, tx, schema, role, password); err != nil {
		return Project{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO projects(id,user_id,name,workspace_path,codex_state_path,last_accessed_at,db_schema,db_username,db_password_ciphertext,deploy_port) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.ID, userID, p.Name, p.WorkspacePath, p.CodexStatePath, p.LastAccessedAt, p.DBSchema, p.DBUsername, p.DBPasswordCiphertext, p.DeployPort); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Project{}, err
	}
	created = true
	return p, nil
}
func (s *projectService) createProjectDatabase(ctx context.Context, tx pgx.Tx, schema, role, password string) error {
	_, err := tx.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD %s", quoteIdent(role), quoteLiteral(password)))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s AUTHORIZATION %s", quoteIdent(schema), quoteIdent(role)))
	return err
}
func quoteIdent(v string) string   { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func quoteLiteral(v string) string { return `'` + strings.ReplaceAll(v, `'`, `''`) + `'` }
func availableProjectPort(base, span int, used []int) (int, bool) {
	occupied := make(map[int]struct{}, len(used))
	for _, port := range used {
		occupied[port] = struct{}{}
	}
	for port := base; port < base+span; port++ {
		if _, exists := occupied[port]; !exists {
			return port, true
		}
	}
	return 0, false
}
func (s *projectService) list(ctx context.Context, userID string) ([]Project, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id,p.name,p.workspace_path,p.codex_state_path,p.db_schema,p.db_username,p.db_password_ciphertext,p.last_accessed_at,COALESCE(p.deploy_port,u.deploy_port,0) FROM projects p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1 ORDER BY p.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.WorkspacePath, &p.CodexStatePath, &p.DBSchema, &p.DBUsername, &p.DBPasswordCiphertext, &p.LastAccessedAt, &p.DeployPort); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *projectService) byID(ctx context.Context, userID, projectID string) (Project, error) {
	var p Project
	err := s.db.QueryRow(ctx, `SELECT p.id,p.name,p.workspace_path,p.codex_state_path,p.db_schema,p.db_username,p.db_password_ciphertext,p.last_accessed_at,COALESCE(p.deploy_port,u.deploy_port,0) FROM projects p JOIN users u ON u.id=p.user_id WHERE p.id=$1 AND p.user_id=$2`, projectID, userID).Scan(&p.ID, &p.Name, &p.WorkspacePath, &p.CodexStatePath, &p.DBSchema, &p.DBUsername, &p.DBPasswordCiphertext, &p.LastAccessedAt, &p.DeployPort)
	return p, err
}
func (s *projectService) ensureRuntime(ctx context.Context, p Project) error {
	lock := s.runtimeLock(p.ID)
	lock.Lock()
	defer lock.Unlock()
	return s.ensureRuntimeLocked(ctx, p)
}
func (s *projectService) ensureRuntimeLocked(ctx context.Context, p Project) error {
	status, err := s.docker.inspect(ctx, runtimeName(p.ID))
	if err != nil {
		log.Printf("ensureRuntime inspect failed project=%s err=%v", p.ID, err)
		return err
	}
	if status.Running {
		return nil
	}
	if status.Exists {
		if err := s.docker.remove(ctx, runtimeName(p.ID)); err != nil {
			log.Printf("ensureRuntime remove failed project=%s err=%v", p.ID, err)
			return err
		}
	}
	password, err := decrypt(s.cfg.MasterKey, p.DBPasswordCiphertext)
	if err != nil {
		log.Printf("ensureRuntime decrypt failed project=%s err=%v", p.ID, err)
		return err
	}
	databaseURL := fmt.Sprintf("postgres://%s:%s@postgres:5432/atoms?sslmode=disable&search_path=%s", url.QueryEscape(p.DBUsername), url.QueryEscape(password), url.QueryEscape(p.DBSchema))
	if err = s.docker.createAndStart(ctx, p, databaseURL, s.cfg.RuntimeImage, s.cfg.DataVolumeName, s.cfg.RuntimeNetwork, strconv.Itoa(p.DeployPort), s.cfg.RuntimeCPU, s.cfg.RuntimeMemoryBytes, s.cfg.RuntimePIDs); err != nil {
		log.Printf("ensureRuntime createAndStart failed project=%s deployPort=%d err=%v", p.ID, p.DeployPort, err)
		return err
	}
	s.touch(ctx, p.ID)
	return nil
}
func (s *projectService) recreateRuntime(ctx context.Context, p Project) error {
	lock := s.runtimeLock(p.ID)
	lock.Lock()
	defer lock.Unlock()
	if err := s.docker.remove(ctx, runtimeName(p.ID)); err != nil {
		return err
	}
	return s.ensureRuntimeLocked(ctx, p)
}
func (s *projectService) removeRuntime(ctx context.Context, projectID string) error {
	lock := s.runtimeLock(projectID)
	lock.Lock()
	defer lock.Unlock()
	return s.docker.remove(ctx, runtimeName(projectID))
}
func (s *projectService) runtimeLock(projectID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.runtimeLocks[projectID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.runtimeLocks[projectID] = lock
	}
	return lock
}
func (s *projectService) hasActiveRun(ctx context.Context, projectID string) (bool, error) {
	var active bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs WHERE project_id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING'))`, projectID).Scan(&active)
	return active, err
}
func waitRuntimeReady(ctx context.Context, projectID string) error {
	deadline := time.NewTimer(120 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	address := net.JoinHostPort(runtimeName(projectID), "3000")
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			log.Printf("waitRuntimeReady ctx done project=%s err=%v", projectID, ctx.Err())
			return ctx.Err()
		case <-deadline.C:
			log.Printf("waitRuntimeReady timeout project=%s", projectID)
			return errRuntimeUnavailable
		case <-ticker.C:
		}
	}
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
		if err := s.removeRuntime(ctx, id); err != nil {
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
				if err := s.cleanup(ctx); err != nil && ctx.Err() == nil {
					log.Printf("runtime cleanup failed: %v", err)
				}
			}
		}
	}()
}
func (s *projectService) recover(ctx context.Context) error {
	ids, err := s.docker.managedProjectIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if err := s.removeRuntime(ctx, id); err != nil {
				return err
			}
		}
	}
	return nil
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

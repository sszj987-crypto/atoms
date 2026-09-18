package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image/png"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"
)

const projectBusySQL = `EXISTS(SELECT 1 FROM agent_runs WHERE project_id=$1 AND status IN ('PENDING','RUNNING','VERIFYING','REPAIRING','CANCELLING')) OR EXISTS(SELECT 1 FROM project_restores WHERE project_id=$1 AND status IN ('PENDING','RUNNING','RECOVERING','BLOCKED'))`
const restoreBusySQL = `EXISTS(SELECT 1 FROM project_restores WHERE project_id=$1 AND status IN ('PENDING','RUNNING','RECOVERING','BLOCKED'))`

var errProjectBusy = errors.New("project is restoring")

type projectQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func projectBusy(ctx context.Context, q projectQuerier, id string) (bool, error) {
	var busy bool
	err := q.QueryRow(ctx, `SELECT `+projectBusySQL, id).Scan(&busy)
	return busy, err
}
func (s *projectService) restoreBusy(ctx context.Context, id string) (bool, error) {
	var busy bool
	err := s.db.QueryRow(ctx, `SELECT `+restoreBusySQL, id).Scan(&busy)
	return busy, err
}
func projectLock(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, id)
	return err
}
func projectOwned(ctx context.Context, tx pgx.Tx, id, userID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1 AND user_id=$2)`, id, userID).Scan(&exists)
	return exists, err
}
func (s *projectService) idleProject(w http.ResponseWriter, r *http.Request, p Project) (pgx.Tx, bool) {
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		apiError(w, 500, "PROJECT_READ_FAILED")
		return nil, false
	}
	if err = projectLock(r.Context(), tx, p.ID); err != nil {
		tx.Rollback(r.Context())
		apiError(w, 500, "PROJECT_READ_FAILED")
		return nil, false
	}
	owned, err := projectOwned(r.Context(), tx, p.ID, r.Context().Value(currentUserKey{}).(User).ID)
	if err != nil || !owned {
		tx.Rollback(r.Context())
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return nil, false
	}
	busy, err := projectBusy(r.Context(), tx, p.ID)
	if err != nil || busy {
		tx.Rollback(r.Context())
		if busy {
			apiError(w, 409, "RUN_IN_PROGRESS")
		} else {
			apiError(w, 500, "RUN_READ_FAILED")
		}
		return nil, false
	}
	return tx, true
}

type projectVersion struct {
	ID           string    `json:"id"`
	Number       int64     `json:"number"`
	Description  string    `json:"description"`
	CreatedAt    time.Time `json:"created_at"`
	HasThumbnail bool      `json:"has_thumbnail"`
	SourceHash   string    `json:"-"`
}
type restoreOperation struct {
	ID              string     `json:"id"`
	VersionID       string     `json:"version_id"`
	Status          string     `json:"status"`
	Phase           string     `json:"phase"`
	ErrorMessage    *string    `json:"error_message,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	PreviousRunning bool       `json:"-"`
	PreviousHash    *string    `json:"-"`
}

const restoreColumns = `id,version_id,status,phase,error_message,created_at,finished_at,previous_runtime_running,previous_source_hash`

func scanRestore(row pgx.Row) (restoreOperation, error) {
	var op restoreOperation
	err := row.Scan(&op.ID, &op.VersionID, &op.Status, &op.Phase, &op.ErrorMessage, &op.CreatedAt, &op.FinishedAt, &op.PreviousRunning, &op.PreviousHash)
	return op, err
}

func (s *projectService) versions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	// A consistent list/current pointer/revision without waiting for file writes.
	if _, err = tx.Exec(r.Context(), `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	var current *string
	var revision int64
	if err = tx.QueryRow(r.Context(), `SELECT current_version_id,source_revision FROM projects WHERE id=$1`, p.ID).Scan(&current, &revision); err != nil {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id,number,description,created_at,has_thumbnail FROM project_versions WHERE project_id=$1 ORDER BY number DESC`, p.ID)
	if err != nil {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	versions := make([]projectVersion, 0)
	for rows.Next() {
		var v projectVersion
		if err = rows.Scan(&v.ID, &v.Number, &v.Description, &v.CreatedAt, &v.HasThumbnail); err != nil {
			break
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	op, err := scanRestore(tx.QueryRow(r.Context(), `SELECT `+restoreColumns+` FROM project_restores WHERE project_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, p.ID))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	var operation *restoreOperation
	if err == nil {
		operation = &op
	}
	busy, err := projectBusy(r.Context(), tx, p.ID)
	if err != nil {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		apiError(w, 500, "VERSION_READ_FAILED")
		return
	}
	writeJSON(w, 200, map[string]any{"versions": versions, "current_version_id": current, "source_revision": revision, "busy": busy, "restore": operation})
}

func (s *projectService) versionThumbnail(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "versionID")
	if !validID(id) {
		apiError(w, 404, "VERSION_NOT_FOUND")
		return
	}
	var has bool
	if err := s.db.QueryRow(r.Context(), `SELECT has_thumbnail FROM project_versions WHERE project_id=$1 AND id=$2`, p.ID, id).Scan(&has); err != nil || !has {
		apiError(w, 404, "VERSION_NOT_FOUND")
		return
	}
	store, err := openVersionStore(p)
	if err != nil {
		apiError(w, 404, "THUMBNAIL_NOT_FOUND")
		return
	}
	defer store.Close()
	f, err := store.OpenFile(id+"/thumbnail.png", unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		apiError(w, 404, "THUMBNAIL_NOT_FOUND")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		apiError(w, 404, "THUMBNAIL_NOT_FOUND")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	http.ServeContent(w, r, "thumbnail.png", time.Time{}, f)
}

// Caller holds the project lock and keeps the run busy until this transaction
// commits. An orphan after a crash/rollback is removed by store garbage collection.
func (s *projectService) saveVersion(ctx context.Context, tx pgx.Tx, p Project, description string, runID *string, thumbnail []byte) (string, error) {
	var number int64
	var parent *string
	if err := tx.QueryRow(ctx, `SELECT next_version_number,current_version_id FROM projects WHERE id=$1`, p.ID).Scan(&number, &parent); err != nil {
		return "", err
	}
	id := newID()
	manifest, err := writeVersionSnapshot(ctx, p, id, thumbnail)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_versions(id,project_id,number,description,run_id,parent_version_id,source_hash,has_thumbnail) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, p.ID, number, description, runID, parent, manifest.Hash, len(thumbnail) > 0)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE projects SET current_version_id=$2,next_version_number=next_version_number+1,source_revision=source_revision+1 WHERE id=$1`, p.ID, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM project_versions WHERE project_id=$1 AND id IN (SELECT id FROM project_versions WHERE project_id=$1 AND id<>$2 ORDER BY number DESC OFFSET 9)`, p.ID, id)
	}
	if err != nil {
		return "", err
	}
	return id, nil
}
func (s *projectService) ensureBaseline(ctx context.Context, tx pgx.Tx, p Project) error {
	var current *string
	if err := tx.QueryRow(ctx, `SELECT current_version_id FROM projects WHERE id=$1`, p.ID).Scan(&current); err != nil {
		return err
	}
	if current != nil {
		return nil
	}
	_, err := s.saveVersion(ctx, tx, p, "项目初始状态", nil, nil)
	return err
}

func (s *projectService) cleanVersionStore(ctx context.Context, p Project) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = projectLock(ctx, tx, p.ID); err != nil {
		return err
	}
	busy, err := projectBusy(ctx, tx, p.ID)
	if err != nil || busy {
		return err
	}
	store, err := openVersionStore(p)
	if err != nil {
		return err
	}
	defer store.Close()
	rows, err := tx.Query(ctx, `SELECT id FROM project_versions WHERE project_id=$1`, p.ID)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		keep[id] = true
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		return fmt.Errorf("version cleanup query failed")
	}
	f, err := store.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		discard := validID(name) && !keep[name] || strings.HasPrefix(name, "pending-") && validID(strings.TrimPrefix(name, "pending-"))
		if strings.HasPrefix(name, "restore-") && validID(strings.TrimPrefix(name, "restore-")) {
			var protected bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project_restores WHERE project_id=$1 AND id=$2 AND status IN ('PENDING','RUNNING','RECOVERING','BLOCKED'))`, p.ID, strings.TrimPrefix(name, "restore-")).Scan(&protected)
			if err != nil {
				return err
			}
			discard = !protected
		}
		if discard {
			if err = store.RemoveAll(name); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *projectService) startRestore(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	var in struct {
		VersionID        string `json:"version_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		RequestID        string `json:"request_id"`
	}
	if decodeJSON(r, &in) != nil || !validID(in.VersionID) || !validID(in.RequestID) || in.ExpectedRevision < 0 {
		apiError(w, 400, "INVALID_RESTORE")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	if err = projectLock(r.Context(), tx, p.ID); err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	owned, err := projectOwned(r.Context(), tx, p.ID, r.Context().Value(currentUserKey{}).(User).ID)
	if err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	if !owned {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	prior, err := scanRestore(tx.QueryRow(r.Context(), `SELECT `+restoreColumns+` FROM project_restores WHERE project_id=$1 AND request_id=$2`, p.ID, in.RequestID))
	if err == nil {
		var expected int64
		if err = tx.QueryRow(r.Context(), `SELECT expected_revision FROM project_restores WHERE id=$1`, prior.ID).Scan(&expected); err != nil {
			apiError(w, 500, "RESTORE_CREATE_FAILED")
			return
		}
		if prior.VersionID != in.VersionID || expected != in.ExpectedRevision {
			apiError(w, 409, "RESTORE_REQUEST_CONFLICT")
			return
		}
		writeJSON(w, 202, map[string]string{"operation_id": prior.ID})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	busy, err := projectBusy(r.Context(), tx, p.ID)
	if err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	if busy {
		apiError(w, 409, "PROJECT_BUSY")
		return
	}
	var revision int64
	var current *string
	if err = tx.QueryRow(r.Context(), `SELECT source_revision,current_version_id FROM projects WHERE id=$1`, p.ID).Scan(&revision, &current); err != nil {
		apiError(w, 404, "PROJECT_NOT_FOUND")
		return
	}
	if revision != in.ExpectedRevision {
		apiError(w, 409, "SOURCE_REVISION_CHANGED")
		return
	}
	if current != nil && *current == in.VersionID {
		apiError(w, 409, "VERSION_ALREADY_CURRENT")
		return
	}
	var exists bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM project_versions WHERE id=$1 AND project_id=$2)`, in.VersionID, p.ID).Scan(&exists); err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	if !exists {
		apiError(w, 404, "VERSION_NOT_FOUND")
		return
	}
	id := newID()
	if _, err = tx.Exec(r.Context(), `INSERT INTO project_restores(id,project_id,version_id,request_id,expected_revision,status) VALUES($1,$2,$3,$4,$5,'PENDING')`, id, p.ID, in.VersionID, in.RequestID, in.ExpectedRevision); err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		apiError(w, 500, "RESTORE_CREATE_FAILED")
		return
	}
	s.launchRestore(p, id)
	writeJSON(w, 202, map[string]string{"operation_id": id})
}
func (s *projectService) getRestore(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "operationID")
	if !validID(id) {
		apiError(w, 404, "RESTORE_NOT_FOUND")
		return
	}
	op, err := scanRestore(s.db.QueryRow(r.Context(), `SELECT `+restoreColumns+` FROM project_restores WHERE project_id=$1 AND id=$2`, p.ID, id))
	if err != nil {
		apiError(w, 404, "RESTORE_NOT_FOUND")
		return
	}
	writeJSON(w, 200, op)
}

type versionRuntime interface {
	Stop(context.Context, Project) error
	Running(context.Context, Project) (bool, error)
	Verify(context.Context, Project) error
	StartPrevious(context.Context, Project) error
	Capture(context.Context, Project) []byte
}
type projectVersionRuntime struct{ s *projectService }

func (v projectVersionRuntime) Stop(ctx context.Context, p Project) error {
	return v.s.removeRuntime(ctx, p.ID)
}
func (v projectVersionRuntime) Running(ctx context.Context, p Project) (bool, error) {
	status, e := v.s.docker.inspect(ctx, runtimeName(p.ID))
	return status.Running, e
}
func (v projectVersionRuntime) StartPrevious(ctx context.Context, p Project) error {
	if e := v.s.ensureRuntime(ctx, p); e != nil {
		return e
	}
	if e := waitRuntimeReady(ctx, p.ID); e != nil {
		return e
	}
	_, e := v.s.docker.exec(ctx, runtimeName(p.ID), []string{"sh", "-lc", "curl --max-time 30 -LfsS http://127.0.0.1:3000/ >/dev/null"}, nil)
	return e
}
func (v projectVersionRuntime) Verify(ctx context.Context, p Project) error {
	if e := v.s.ensureRuntime(ctx, p); e != nil {
		return e
	}
	if e := waitRuntimeReady(ctx, p.ID); e != nil {
		return e
	}
	for _, check := range []struct{ command, reason string }{{"pnpm typecheck", "历史版本类型检查失败"}, {"pnpm build", "历史版本生产构建失败"}} {
		if _, e := v.s.docker.exec(ctx, runtimeName(p.ID), []string{"sh", "-lc", check.command}, nil); e != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &restoreCheckError{check.reason}
		}
	}
	// Runtime startup has already installed the restored lockfile. Its private
	// dev build directory is separate from pnpm build, so production verification
	// does not require another container restart (and dependency install).
	if _, e := v.s.docker.exec(ctx, runtimeName(p.ID), []string{"sh", "-lc", "curl --max-time 30 -LfsS http://127.0.0.1:3000/ >/dev/null"}, nil); e != nil {
		return &restoreCheckError{"历史版本首页 HTTP 检查失败"}
	}
	return nil
}
func (v projectVersionRuntime) Capture(ctx context.Context, p Project) []byte {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	out, e := v.s.docker.exec(ctx, runtimeName(p.ID), []string{"atoms-version-screenshot"}, nil)
	if e != nil {
		return nil
	}
	encoded := strings.Join(strings.Fields(string(out)), "")
	if len(encoded) > 4<<20 {
		return nil
	}
	raw, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil || len(raw) > 3<<20 {
		return nil
	}
	config, e := png.DecodeConfig(bytes.NewReader(raw))
	if e != nil || config.Width != 1280 || config.Height != 720 {
		return nil
	}
	return raw
}
func (s *projectService) collectVersions(ctx context.Context) {
	rows, err := s.db.Query(ctx, `SELECT p.id,p.workspace_path FROM projects p WHERE NOT (`+strings.ReplaceAll(projectBusySQL, "project_id=$1", "project_id=p.id")+`)`)
	if err != nil {
		log.Printf("version cleanup query failed")
		return
	}
	var projects []Project
	for rows.Next() {
		var p Project
		if rows.Scan(&p.ID, &p.WorkspacePath) == nil {
			projects = append(projects, p)
		}
	}
	rows.Close()
	for _, p := range projects {
		if err = s.cleanVersionStore(ctx, p); err != nil {
			log.Printf("version cleanup failed project_id=%s", p.ID)
		}
	}
}

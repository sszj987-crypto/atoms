package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type restoreContextKey struct{}
type restoreCheckError struct{ reason string }

func (e *restoreCheckError) Error() string { return e.reason }

var errRestoreSourceChanged = errors.New("restored source hash mismatch")

func (s *projectService) restoreRuntime() versionRuntime {
	if s.versionRuntime != nil {
		return s.versionRuntime
	}
	return projectVersionRuntime{s}
}
func (s *projectService) launchRestore(p Project, id string) {
	s.mu.Lock()
	if s.restoreCtx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.restoreWorkers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.restoreWorkers.Done()
		ctx, cancel := context.WithTimeout(context.WithValue(s.restoreCtx, restoreContextKey{}, p.ID), 15*time.Minute)
		defer cancel()
		tag, err := s.db.Exec(ctx, `UPDATE project_restores SET status='RUNNING' WHERE id=$1 AND status='PENDING'`, id)
		if err != nil || tag.RowsAffected() == 0 {
			return
		}
		if err = s.executeRestore(ctx, p, id); err != nil {
			if s.restoreCtx.Err() != nil {
				return
			} // Startup repairs the persisted journal.
			recoveryCtx, cancel := context.WithTimeout(context.WithValue(context.Background(), restoreContextKey{}, p.ID), 3*time.Minute)
			defer cancel()
			var phase string
			_ = s.db.QueryRow(recoveryCtx, `SELECT phase FROM project_restores WHERE id=$1`, id).Scan(&phase)
			message := restoreFailureMessage(phase, err)
			if recoveryErr := s.recoverRestore(recoveryCtx, p, id, message); recoveryErr != nil {
				log.Printf("restore recovery failed project_id=%s operation_id=%s", p.ID, id)
			}
		} else {
			if err = s.cleanVersionStore(ctx, p); err != nil {
				log.Printf("restore cleanup deferred project_id=%s", p.ID)
			}
		}
	}()
}

// Do not expose raw filesystem, Docker, build output, or credentials to clients.
func restoreFailureMessage(phase string, err error) string {
	reason := map[string]string{"PREPARING": "历史快照校验失败", "STOPPING": "旧预览停止或私有配置保留失败", "BACKING_UP": "原工作区备份失败", "SWAPPING": "工作区替换失败", "VERIFYING": "依赖安装、类型检查、构建或首页检查失败", "SESSION_HANDOFF": "开发会话隔离或预览启动失败", "COMMITTING": "版本状态保存失败"}[phase]
	if reason == "" {
		reason = "版本恢复失败"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		reason = "版本恢复超时"
	}
	var check *restoreCheckError
	if errors.As(err, &check) {
		reason = check.reason
	}
	if errors.Is(err, errRestoreSourceChanged) {
		reason = "恢复后的源码与历史快照校验值不一致"
	}
	return reason + "，已还原原项目。"
}

func syncRestoreDirectories(parent *os.Root, names ...string) error {
	if err := syncDirectory(parent); err != nil {
		return err
	}
	for _, name := range names {
		root, err := parent.OpenRoot(name)
		if err != nil {
			return err
		}
		err = syncDirectory(root)
		root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *projectService) shutdownRestores(ctx context.Context) error {
	s.mu.Lock()
	s.restoreCancel()
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.restoreWorkers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *projectService) restorePhase(ctx context.Context, id, phase string) error {
	_, err := s.db.Exec(ctx, `UPDATE project_restores SET phase=$2 WHERE id=$1 AND status IN ('RUNNING','RECOVERING','BLOCKED')`, id, phase)
	return err
}

func (s *projectService) restoreProjectRoot(p Project) (*os.Root, error) {
	if !validID(p.ID) || filepath.Base(p.WorkspacePath) != "workspace" || p.CodexStatePath != filepath.Join(filepath.Dir(p.WorkspacePath), "codex") {
		return nil, errSourcePath
	}
	if s.cfg.ProjectRoot != "" {
		relative, err := filepath.Rel(filepath.Clean(s.cfg.ProjectRoot), filepath.Dir(p.WorkspacePath))
		if err != nil || filepath.IsAbs(relative) {
			return nil, errSourcePath
		}
		if filepath.Base(filepath.Dir(p.WorkspacePath)) != p.ID {
			return nil, errSourcePath
		}
		// Paths originate from the server, but still refuse an out-of-root record.
		if relative == ".." || len(relative) > 3 && relative[:3] == ".."+string(filepath.Separator) {
			return nil, errSourcePath
		}
	}
	return os.OpenRoot(filepath.Dir(p.WorkspacePath))
}
func existingDirectory(root *os.Root, name string) (bool, error) {
	f, e := root.OpenFile(name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if errors.Is(e, fs.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	f.Close()
	return true, nil
}
func runtimeOwnership(root *os.Root) error {
	if os.Geteuid() != 0 {
		return nil
	}
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errSourcePath
		}
		return root.Chown(name, 1000, 1000)
	})
}

func (s *projectService) executeRestore(ctx context.Context, p Project, id string) error {
	runtime := s.restoreRuntime()
	running, err := runtime.Running(ctx, p)
	if err != nil {
		return err
	}
	if _, err = s.db.Exec(ctx, `UPDATE project_restores SET previous_runtime_running=$2 WHERE id=$1`, id, running); err != nil {
		return err
	}
	var versionID, hash string
	if err = s.db.QueryRow(ctx, `SELECT v.id,v.source_hash FROM project_versions v JOIN project_restores r ON r.project_id=v.project_id AND r.version_id=v.id WHERE r.id=$1 AND r.project_id=$2`, id, p.ID).Scan(&versionID, &hash); err != nil {
		return err
	}
	parent, err := s.restoreProjectRoot(p)
	if err != nil {
		return err
	}
	defer parent.Close()
	store, err := openVersionStore(p)
	if err != nil {
		return err
	}
	defer store.Close()
	job := "restore-" + id
	if err = store.Mkdir(job, 0700); err != nil {
		return err
	}
	if err = store.Mkdir(job+"/next", 0750); err != nil {
		return err
	}
	if err = syncDirectory(store); err != nil {
		return err
	}
	next, err := store.OpenRoot(job + "/next")
	if err != nil {
		return err
	}
	defer next.Close()
	if err = extractVersion(ctx, store, versionID, hash, next); err != nil {
		return err
	}
	if err = s.restorePhase(ctx, id, "STOPPING"); err != nil {
		return err
	}
	if err = runtime.Stop(ctx, p); err != nil {
		return err
	}
	// Once stopped, preserve current private configuration into the clean tree.
	check, err := parent.OpenFile("workspace", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	check.Close()
	original, err := parent.OpenRoot("workspace")
	if err != nil {
		return err
	}
	err = preserveWorkspace(ctx, original, next)
	original.Close()
	if err != nil {
		return err
	}
	if err = runtimeOwnership(next); err != nil {
		return err
	}
	if err = syncTreeDirectories(ctx, next); err != nil {
		return err
	}
	prior, err := sourceManifest(ctx, p.WorkspacePath)
	if err != nil {
		return err
	}
	if _, err = s.db.Exec(ctx, `UPDATE project_restores SET previous_source_hash=$2 WHERE id=$1`, id, prior.Hash); err != nil {
		return err
	}
	if err = s.restorePhase(ctx, id, "BACKING_UP"); err != nil {
		return err
	}
	if err = parent.Rename("workspace", "versions/"+job+"/previous"); err != nil {
		return err
	}
	if err = syncRestoreDirectories(parent, "versions/"+job); err != nil {
		return err
	}
	if err = s.restorePhase(ctx, id, "SWAPPING"); err != nil {
		return err
	}
	if err = parent.Rename("versions/"+job+"/next", "workspace"); err != nil {
		return err
	}
	if err = syncRestoreDirectories(parent, "versions/"+job); err != nil {
		return err
	}
	if err = s.restorePhase(ctx, id, "VERIFYING"); err != nil {
		return err
	}
	if err = runtime.Verify(ctx, p); err != nil {
		return err
	}
	current, err := sourceManifest(ctx, p.WorkspacePath)
	if err != nil {
		return err
	}
	if current.Hash != hash {
		return errRestoreSourceChanged
	}
	if err = s.restorePhase(ctx, id, "SESSION_HANDOFF"); err != nil {
		return err
	}
	if err = runtime.Stop(ctx, p); err != nil {
		return err
	}
	if err = store.Mkdir("sessions", 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err = parent.Rename("codex", "versions/sessions/"+id); err != nil {
		return err
	}
	if err = parent.Mkdir("codex", 0750); err != nil {
		return err
	}
	session, err := parent.OpenRoot("codex")
	if err != nil {
		return err
	}
	err = runtimeOwnership(session)
	session.Close()
	if err != nil {
		return err
	}
	if err = syncRestoreDirectories(parent, "versions", "versions/sessions"); err != nil {
		return err
	}
	if err = runtime.StartPrevious(ctx, p); err != nil {
		return err
	}
	current, err = sourceManifest(ctx, p.WorkspacePath)
	if err != nil {
		return err
	}
	if current.Hash != hash {
		return errRestoreSourceChanged
	}
	if err = s.restorePhase(ctx, id, "COMMITTING"); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = projectLock(ctx, tx, p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE projects SET current_version_id=$2,source_revision=source_revision+1 WHERE id=$1`, p.ID, versionID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE project_restores SET status='COMPLETED',phase='COMPLETED',finished_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *projectService) recoverRestore(ctx context.Context, p Project, id, message string) (result error) {
	op, err := scanRestore(s.db.QueryRow(ctx, `SELECT `+restoreColumns+` FROM project_restores WHERE id=$1 AND project_id=$2`, id, p.ID))
	if err != nil {
		return err
	}
	if op.Status == "COMPLETED" || op.Status == "FAILED" {
		return nil
	}
	defer func() {
		if result != nil {
			protectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, e := s.db.Exec(protectCtx, `UPDATE project_restores SET status='BLOCKED',error_message='恢复和还原未能全部完成，项目已保护，请检查运行环境并重启服务重试。' WHERE id=$1`, id)
			if e != nil {
				log.Printf("restore protection persistence failed operation_id=%s", id)
			}
		}
	}()
	if _, err = s.db.Exec(ctx, `UPDATE project_restores SET status='RECOVERING',error_message=$2 WHERE id=$1`, id, message); err != nil {
		return err
	}
	if op.Phase != "PREPARING" {
		runtime := s.restoreRuntime()
		if err = runtime.Stop(ctx, p); err != nil {
			return err
		}
		parent, e := s.restoreProjectRoot(p)
		if e != nil {
			return e
		}
		defer parent.Close()
		job := "versions/restore-" + id
		previous, e := existingDirectory(parent, job+"/previous")
		if e != nil {
			return e
		}
		if previous {
			current, e := existingDirectory(parent, "workspace")
			if e != nil {
				return e
			}
			if current {
				if e = parent.Rename("workspace", job+"/rejected"); e != nil {
					return e
				}
			}
			if e = parent.Rename(job+"/previous", "workspace"); e != nil {
				return e
			}
			if e = syncRestoreDirectories(parent, job); e != nil {
				return e
			}
		}
		exists, e := existingDirectory(parent, "workspace")
		if e != nil {
			return e
		}
		if !exists {
			return fmt.Errorf("missing original workspace")
		}
		if op.PreviousHash != nil {
			manifest, e := sourceManifest(ctx, p.WorkspacePath)
			if e != nil {
				return e
			}
			if manifest.Hash != *op.PreviousHash {
				return fmt.Errorf("original workspace verification failed")
			}
		}
		archived, e := existingDirectory(parent, "versions/sessions/"+id)
		if e != nil {
			return e
		}
		if archived {
			current, e := existingDirectory(parent, "codex")
			if e != nil {
				return e
			}
			if current {
				if e = parent.Rename("codex", job+"/rejected-session"); e != nil {
					return e
				}
			}
			if e = parent.Rename("versions/sessions/"+id, "codex"); e != nil {
				return e
			}
			if e = syncRestoreDirectories(parent, job, "versions/sessions"); e != nil {
				return e
			}
		}
		if op.PreviousRunning {
			if e = runtime.StartPrevious(ctx, p); e != nil {
				return e
			}
			if op.PreviousHash != nil {
				manifest, e := sourceManifest(ctx, p.WorkspacePath)
				if e != nil {
					return e
				}
				if manifest.Hash != *op.PreviousHash {
					return fmt.Errorf("original preview changed restored source")
				}
			}
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = projectLock(ctx, tx, p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE projects SET source_revision=source_revision+1 WHERE id=$1`, p.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE project_restores SET status='FAILED',phase='FAILED',error_message=$2,finished_at=now() WHERE id=$1`, id, message); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if err = s.cleanVersionStore(ctx, p); err != nil {
		log.Printf("recovery cleanup deferred project_id=%s", p.ID)
	}
	return nil
}

func (s *projectService) recoverRestores(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT r.id,p.id,p.workspace_path,p.codex_state_path,p.db_schema,p.db_username,p.db_password_ciphertext,COALESCE(p.deploy_port,0),p.deployed FROM project_restores r JOIN projects p ON p.id=r.project_id WHERE r.status IN ('PENDING','RUNNING','RECOVERING','BLOCKED')`)
	if err != nil {
		return err
	}
	type pending struct {
		id string
		p  Project
	}
	var operations []pending
	for rows.Next() {
		var x pending
		if err = rows.Scan(&x.id, &x.p.ID, &x.p.WorkspacePath, &x.p.CodexStatePath, &x.p.DBSchema, &x.p.DBUsername, &x.p.DBPasswordCiphertext, &x.p.DeployPort, &x.p.Deployed); err != nil {
			break
		}
		operations = append(operations, x)
	}
	rows.Close()
	if err != nil || rows.Err() != nil {
		return fmt.Errorf("restore recovery read failed")
	}
	for _, x := range operations {
		recoveryCtx, cancel := context.WithTimeout(context.WithValue(ctx, restoreContextKey{}, x.p.ID), 3*time.Minute)
		err = s.recoverRestore(recoveryCtx, x.p, x.id, "服务重启中断了恢复，已还原原项目。")
		cancel()
		if err != nil {
			log.Printf("restore remains protected project_id=%s", x.p.ID)
		}
	}
	s.collectVersions(ctx)
	return nil
}

package platform

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"
)

const (
	maxSourcePreviewBytes = 1 << 20
	maxSourceExportBytes  = 100 << 20
	maxSourceExportFiles  = 10000
	maxSourceEntries      = 20000
)

var (
	errSourcePath  = errors.New("invalid source path")
	errSourceLimit = errors.New("source limit exceeded")
)

type sourceEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type sourceFile struct {
	sourceEntry
	Previewable bool   `json:"previewable"`
	Reason      string `json:"reason,omitempty"`
	Content     string `json:"content,omitempty"`
}

// A single policy applies to listings, reads and both kinds of download.
func sourcePathAllowed(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:") || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		lower := strings.ToLower(part)
		switch lower {
		case "node_modules", ".next", ".next-dev", ".next-preview", "dist", "build", "out", "coverage", ".cache", ".turbo", ".vite", ".pnpm-store", ".git", ".codex", "codex", ".atoms", "logs", ".ssh", ".aws", ".vercel", ".netlify", ".npmrc", ".pnpmrc", ".yarnrc", ".yarnrc.yml", ".ds_store", "id_rsa", "id_ed25519", "credentials.json", "secrets.json":
			return false
		}
		if strings.HasPrefix(lower, ".atoms-") || strings.HasPrefix(lower, ".env") && lower != ".env.example" && lower != ".env.sample" && lower != ".env.template" {
			return false
		}
		for _, suffix := range []string{".log", ".tsbuildinfo", ".pem", ".key", ".p12", ".pfx"} {
			if strings.HasSuffix(lower, suffix) {
				return false
			}
		}
	}
	return true
}

// os.Root anchors access to the workspace. Opening each component with
// O_NOFOLLOW also rejects in-root links and link-swap races. O_NONBLOCK keeps
// a file changed into a FIFO from blocking the HTTP handler.
func openSource(root *os.Root, name string, directory bool) (*os.File, error) {
	if name != "." && !sourcePathAllowed(name) {
		return nil, errSourcePath
	}
	current, err := root.OpenFile(".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if name == "." {
		if directory {
			return current, nil
		}
		current.Close()
		return nil, errSourcePath
	}
	parts := strings.Split(name, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
		if i < len(parts)-1 || directory {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(current.Fd()), part, flags, 0)
		current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(fd), filepath.Join(root.Name(), filepath.FromSlash(strings.Join(parts[:i+1], "/"))))
	}
	info, err := current.Stat()
	if err != nil || directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		current.Close()
		if err != nil {
			return nil, err
		}
		return nil, errSourcePath
	}
	return current, nil
}

func listSource(ctx context.Context, root *os.Root) ([]sourceEntry, error) {
	entries := make([]sourceEntry, 0)
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if depth > 128 {
			return errSourceLimit
		}
		folder, err := openSource(root, dir, true)
		if err != nil {
			return err
		}
		defer folder.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			children, err := folder.ReadDir(128)
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			for _, child := range children {
				name := path.Join(dir, child.Name())
				if !sourcePathAllowed(name) || child.Type()&os.ModeSymlink != 0 {
					continue
				}
				opened, err := openSource(root, name, child.IsDir())
				if errors.Is(err, fs.ErrNotExist) { // The running developer may remove files.
					continue
				}
				if errors.Is(err, errSourcePath) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
					continue
				}
				if err != nil {
					return err
				}
				info, err := opened.Stat()
				opened.Close()
				if err != nil {
					return err
				}
				if !info.IsDir() && !info.Mode().IsRegular() {
					continue
				}
				if len(entries) >= maxSourceEntries {
					return errSourceLimit
				}
				entry := sourceEntry{Path: name, IsDir: info.IsDir(), Size: info.Size()}
				if entry.IsDir {
					entry.Size = 0
				}
				entries = append(entries, entry)
				if entry.IsDir {
					if err := walk(name, depth+1); err != nil {
						return err
					}
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
		}
		return nil
	}
	if err := walk(".", 0); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func readSource(root *os.Root, name string) (sourceFile, error) {
	file, err := openSource(root, name, false)
	if err != nil {
		return sourceFile{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return sourceFile{}, err
	}
	result := sourceFile{sourceEntry: sourceEntry{Path: name, Size: info.Size()}}
	if info.Size() > maxSourcePreviewBytes {
		result.Reason = "too_large"
		return result, nil
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSourcePreviewBytes+1))
	if err != nil {
		return sourceFile{}, err
	}
	result.Size = int64(len(raw))
	if len(raw) > maxSourcePreviewBytes {
		result.Reason = "too_large"
	} else if !utf8.Valid(raw) || strings.ContainsFunc(string(raw), func(r rune) bool {
		return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t'
	}) {
		result.Reason = "binary"
	} else {
		result.Previewable = true
		result.Content = string(raw)
	}
	return result, nil
}

type contextSourceReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextSourceReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func copySource(ctx context.Context, dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(contextSourceReader{ctx, src}, limit+1))
	if err == nil && n > limit {
		return n, errSourceLimit
	}
	return n, err
}

func zipSource(ctx context.Context, root *os.Root, dst io.Writer) error {
	entries, err := listSource(ctx, root)
	if err != nil {
		return err
	}
	count := 0
	var total int64
	for _, entry := range entries {
		if !entry.IsDir {
			count++
			total += entry.Size
		}
	}
	if count > maxSourceExportFiles || total > maxSourceExportBytes {
		return errSourceLimit
	}
	archive := zip.NewWriter(dst)
	defer archive.Close()
	total = 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir {
			if _, err := archive.Create(entry.Path + "/"); err != nil {
				return err
			}
			continue
		}
		file, err := openSource(root, entry.Path, false)
		if err != nil {
			return err
		}
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: entry.Path, Method: zip.Deflate})
		if err != nil {
			file.Close()
			return err
		}
		n, err := copySource(ctx, writer, file, maxSourceExportBytes-total)
		file.Close()
		if err != nil {
			return err
		}
		total += n
	}
	return archive.Close()
}

func privateSourceResponse(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func sourceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSourcePath), errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR), errors.Is(err, fs.ErrPermission):
		apiError(w, http.StatusBadRequest, "INVALID_FILE_PATH")
	case errors.Is(err, fs.ErrNotExist):
		apiError(w, http.StatusNotFound, "FILE_NOT_FOUND")
	case errors.Is(err, errSourceLimit):
		apiError(w, http.StatusRequestEntityTooLarge, "SOURCE_LIMIT_EXCEEDED")
	default:
		apiError(w, http.StatusInternalServerError, "FILE_READ_FAILED")
	}
}

func (s *projectService) sourceProject(w http.ResponseWriter, r *http.Request) (Project, bool) {
	privateSourceResponse(w)
	p, err := s.byID(r.Context(), r.Context().Value(currentUserKey{}).(User).ID, chi.URLParam(r, "id"))
	if errors.Is(err, pgx.ErrNoRows) {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return Project{}, false
	}
	if err != nil {
		apiError(w, http.StatusInternalServerError, "PROJECT_READ_FAILED")
		return Project{}, false
	}
	s.touch(r.Context(), p.ID)
	return p, true
}

func (s *projectService) files(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	tx, ok := s.sourceReadLock(w, r, p)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	root, err := os.OpenRoot(p.WorkspacePath)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer root.Close()
	entries, err := listSource(r.Context(), root)
	if err != nil {
		sourceError(w, err)
		return
	}
	active, err := s.hasActiveRun(r.Context(), p.ID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "RUN_READ_FAILED")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": entries, "run_active": active})
}

func (s *projectService) file(w http.ResponseWriter, r *http.Request) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	tx, ok := s.sourceReadLock(w, r, p)
	if !ok {
		return
	}
	defer tx.Rollback(r.Context())
	root, err := os.OpenRoot(p.WorkspacePath)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer root.Close()
	result, err := readSource(root, r.URL.Query().Get("path"))
	if err != nil {
		sourceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *projectService) downloadFile(w http.ResponseWriter, r *http.Request) {
	s.sourceDownload(w, r, false)
}

func (s *projectService) exportSource(w http.ResponseWriter, r *http.Request) {
	s.sourceDownload(w, r, true)
}

func (s *projectService) sourceDownload(w http.ResponseWriter, r *http.Request, archive bool) {
	p, ok := s.sourceProject(w, r)
	if !ok {
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		apiError(w, http.StatusInternalServerError, "SOURCE_EXPORT_FAILED")
		return
	}
	defer tx.Rollback(r.Context())
	// This is the same lock used by chat.send and project.delete. Snapshot bytes
	// while holding it, but never hold it for a slow client downloading the result.
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext($1))`, p.ID); err != nil {
		apiError(w, http.StatusInternalServerError, "SOURCE_EXPORT_FAILED")
		return
	}
	var exists, active bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM projects WHERE id=$1 AND user_id=$2), `+projectBusySQL, p.ID, r.Context().Value(currentUserKey{}).(User).ID).Scan(&exists, &active)
	if err != nil {
		apiError(w, http.StatusInternalServerError, "SOURCE_EXPORT_FAILED")
		return
	}
	if !exists {
		apiError(w, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	if active {
		apiError(w, http.StatusConflict, "RUN_IN_PROGRESS")
		return
	}
	root, err := os.OpenRoot(p.WorkspacePath)
	if err != nil {
		sourceError(w, err)
		return
	}
	defer root.Close()
	temp, err := os.CreateTemp("", "atoms-source-*")
	if err != nil {
		apiError(w, http.StatusInternalServerError, "SOURCE_EXPORT_FAILED")
		return
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	filename := p.Name + "-source.zip"
	contentType := "application/zip"
	if archive {
		err = zipSource(r.Context(), root, temp)
	} else {
		name := r.URL.Query().Get("path")
		var file *os.File
		file, err = openSource(root, name, false)
		if err == nil {
			_, err = copySource(r.Context(), temp, file, maxSourceExportBytes)
			file.Close()
		}
		filename, contentType = path.Base(name), "application/octet-stream"
	}
	if err != nil {
		sourceError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		apiError(w, http.StatusInternalServerError, "SOURCE_EXPORT_FAILED")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	http.ServeContent(w, r, filename, time.Time{}, temp)
}
func (s *projectService) sourceReadLock(w http.ResponseWriter, r *http.Request, p Project) (pgx.Tx, bool) {
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		apiError(w, 500, "FILE_READ_FAILED")
		return nil, false
	}
	if err = projectLock(r.Context(), tx, p.ID); err != nil {
		tx.Rollback(r.Context())
		apiError(w, 500, "FILE_READ_FAILED")
		return nil, false
	}
	var busy bool
	if err = tx.QueryRow(r.Context(), `SELECT `+restoreBusySQL, p.ID).Scan(&busy); err != nil || busy {
		tx.Rollback(r.Context())
		if busy {
			apiError(w, 409, "PROJECT_RESTORING")
		} else {
			apiError(w, 500, "FILE_READ_FAILED")
		}
		return nil, false
	}
	return tx, true
}

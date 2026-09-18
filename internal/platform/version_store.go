package platform

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type versionEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Hash  string `json:"hash,omitempty"`
	Mode  uint32 `json:"mode"`
}
type versionManifest struct {
	Entries []versionEntry `json:"entries"`
	Hash    string         `json:"hash"`
}

var errVersionCorrupt = errors.New("invalid version snapshot")

func validID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil && len(raw) == 16 && strings.ToLower(id) == id
}

// Store directories are outside the workspace and never mounted in a Runtime.
func openVersionStore(p Project) (*os.Root, error) {
	parent, err := os.OpenRoot(filepath.Dir(p.WorkspacePath))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err = parent.Mkdir("versions", 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	check, err := parent.OpenFile("versions", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	check.Close()
	if err = syncDirectory(parent); err != nil {
		return nil, err
	}
	return parent.OpenRoot("versions")
}
func syncDirectory(root *os.Root) error {
	f, e := root.Open(".")
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

func syncTreeDirectories(ctx context.Context, root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errSourcePath
		}
		if !entry.IsDir() {
			return nil
		}
		dir, err := root.OpenRoot(name)
		if err != nil {
			return err
		}
		err = syncDirectory(dir)
		dir.Close()
		return err
	})
}
func manifestHash(entries []versionEntry) string {
	// Private configuration may require extra empty parents, not extra source.
	files := make([]versionEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir {
			files = append(files, entry)
		}
	}
	raw, _ := json.Marshal(files)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func buildVersion(ctx context.Context, root *os.Root, dst io.Writer) (versionManifest, error) {
	entries, err := listSource(ctx, root)
	if err != nil {
		return versionManifest{}, err
	}
	result := versionManifest{Entries: make([]versionEntry, 0, len(entries))}
	archive := zip.NewWriter(dst)
	var total int64
	count := 0
	for _, entry := range entries {
		item := versionEntry{Path: entry.Path, IsDir: entry.IsDir, Mode: 0750}
		header := &zip.FileHeader{Name: entry.Path, Method: zip.Deflate}
		if entry.IsDir {
			header.Name += "/"
			header.SetMode(os.ModeDir | 0750)
			if _, err = archive.CreateHeader(header); err != nil {
				return result, err
			}
			result.Entries = append(result.Entries, item)
			continue
		}
		count++
		if count > maxSourceExportFiles {
			return result, errSourceLimit
		}
		file, e := openSource(root, entry.Path, false)
		if e != nil {
			return result, e
		}
		info, e := file.Stat()
		if e != nil {
			file.Close()
			return result, e
		}
		item.Mode = 0640
		if info.Mode().Perm()&0111 != 0 {
			item.Mode = 0750
		}
		header.SetMode(os.FileMode(item.Mode))
		writer, e := archive.CreateHeader(header)
		if e != nil {
			file.Close()
			return result, e
		}
		hash := sha256.New()
		n, e := copySource(ctx, io.MultiWriter(writer, hash), file, maxSourceExportBytes-total)
		file.Close()
		if e != nil {
			return result, e
		}
		total += n
		item.Size = n
		item.Hash = hex.EncodeToString(hash.Sum(nil))
		result.Entries = append(result.Entries, item)
	}
	if err = archive.Close(); err != nil {
		return result, err
	}
	result.Hash = manifestHash(result.Entries)
	return result, nil
}

func sourceManifest(ctx context.Context, workspace string) (versionManifest, error) {
	root, e := os.OpenRoot(workspace)
	if e != nil {
		return versionManifest{}, e
	}
	defer root.Close()
	// Reuse the exact ZIP enumeration and hashing policy without retaining bytes.
	return buildVersion(ctx, root, io.Discard)
}

func writeVersionSnapshot(ctx context.Context, p Project, id string, thumbnail []byte) (versionManifest, error) {
	if !validID(id) {
		return versionManifest{}, errVersionCorrupt
	}
	store, err := openVersionStore(p)
	if err != nil {
		return versionManifest{}, err
	}
	defer store.Close()
	temp := "pending-" + id
	if err = store.Mkdir(temp, 0700); err != nil {
		return versionManifest{}, err
	}
	success := false
	defer func() {
		if !success {
			store.RemoveAll(temp)
		}
	}()
	root, err := os.OpenRoot(p.WorkspacePath)
	if err != nil {
		return versionManifest{}, err
	}
	defer root.Close()
	out, err := store.OpenFile(temp+"/source.zip", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return versionManifest{}, err
	}
	manifest, err := buildVersion(ctx, root, out)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return manifest, err
	}
	current, err := sourceManifest(ctx, p.WorkspacePath)
	if err != nil {
		return manifest, err
	}
	if current.Hash != manifest.Hash {
		return manifest, fmt.Errorf("source changed while saving version")
	}
	raw, _ := json.Marshal(manifest)
	if err = writeStoreFile(store, temp+"/manifest.json", raw); err != nil {
		return manifest, err
	}
	if len(thumbnail) > 0 {
		if err = writeStoreFile(store, temp+"/thumbnail.png", thumbnail); err != nil {
			return manifest, err
		}
	}
	dir, err := store.OpenRoot(temp)
	if err != nil {
		return manifest, err
	}
	err = syncDirectory(dir)
	dir.Close()
	if err != nil {
		return manifest, err
	}
	if err = store.Rename(temp, id); err != nil {
		return manifest, err
	}
	success = true
	return manifest, syncDirectory(store)
}
func writeStoreFile(root *os.Root, name string, raw []byte) error {
	f, e := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(raw); e != nil {
		return e
	}
	return f.Sync()
}

func loadVersionManifest(store *os.Root, id, expectedHash string) (versionManifest, error) {
	var manifest versionManifest
	if !validID(id) {
		return manifest, errVersionCorrupt
	}
	f, e := store.OpenFile(id+"/manifest.json", unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return manifest, e
	}
	defer f.Close()
	if e = json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(&manifest); e != nil {
		return manifest, errVersionCorrupt
	}
	if len(manifest.Entries) > maxSourceEntries || manifest.Hash != expectedHash || manifestHash(manifest.Entries) != expectedHash {
		return manifest, errVersionCorrupt
	}
	seen := map[string]bool{}
	var total int64
	count := 0
	for _, entry := range manifest.Entries {
		if !sourcePathAllowed(entry.Path) || seen[entry.Path] || strings.Count(entry.Path, "/") > 128 || entry.Size < 0 {
			return manifest, errVersionCorrupt
		}
		seen[entry.Path] = true
		if entry.IsDir {
			if entry.Size != 0 || entry.Mode != 0750 || entry.Hash != "" {
				return manifest, errVersionCorrupt
			}
		} else {
			count++
			total += entry.Size
			if entry.Size > maxSourceExportBytes || total > maxSourceExportBytes || count > maxSourceExportFiles || len(entry.Hash) != 64 || entry.Mode != 0640 && entry.Mode != 0750 {
				return manifest, errVersionCorrupt
			}
		}
	}
	return manifest, nil
}

// Only internally produced, manifest-checked ordinary files are extracted.
func extractVersion(ctx context.Context, store *os.Root, id, hash string, dest *os.Root) error {
	manifest, e := loadVersionManifest(store, id, hash)
	if e != nil {
		return e
	}
	f, e := store.OpenFile(id+"/source.zip", unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return errVersionCorrupt
	}
	archive, e := zip.NewReader(f, info.Size())
	if e != nil {
		return errVersionCorrupt
	}
	if len(archive.File) != len(manifest.Entries) {
		return errVersionCorrupt
	}
	for i, item := range manifest.Entries {
		file := archive.File[i]
		name := item.Path
		if item.IsDir {
			name += "/"
		}
		if file.Name != name || item.IsDir != file.FileInfo().IsDir() || !item.IsDir && !file.Mode().IsRegular() || file.Mode()&os.ModeSymlink != 0 || file.UncompressedSize64 != uint64(item.Size) {
			return errVersionCorrupt
		}
		if item.IsDir {
			if e = dest.Mkdir(item.Path, 0750); e != nil {
				return e
			}
			continue
		}
		reader, e := file.Open()
		if e != nil {
			return e
		}
		out, e := dest.OpenFile(item.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(item.Mode))
		if e != nil {
			reader.Close()
			return e
		}
		digest := sha256.New()
		n, e := copySource(ctx, io.MultiWriter(out, digest), reader, item.Size)
		reader.Close()
		if e == nil {
			e = out.Sync()
		}
		out.Close()
		if e != nil {
			return e
		}
		if n != item.Size || hex.EncodeToString(digest.Sum(nil)) != item.Hash {
			return errVersionCorrupt
		}
	}
	return syncDirectory(dest)
}

// Keep excluded configuration, not dependencies or generated caches. Traversal
// is anchored at the stopped workspace; links and special files are rejected.
func preserveWorkspace(ctx context.Context, source, dest *os.Root) error {
	var walk func(string, bool, int) error
	walk = func(dir string, excluded bool, depth int) error {
		if depth > 128 {
			return errSourceLimit
		}
		f, e := source.OpenFile(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if e != nil {
			return e
		}
		entries, e := f.ReadDir(-1)
		f.Close()
		if e != nil {
			return e
		}
		for _, entry := range entries {
			name := entry.Name()
			if dir != "." {
				name = dir + "/" + name
			}
			if discardRestoredPath(name) {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errSourcePath
			}
			isExcluded := excluded || !sourcePathAllowed(name)
			if entry.IsDir() {
				// Ensure parents of protected files exist, even if absent in the version.
				if isExcluded {
					if e = mkdirRootParents(dest, name); e != nil {
						return e
					}
				}
				if e = walk(name, isExcluded, depth+1); e != nil {
					return e
				}
				continue
			}
			if !isExcluded {
				continue
			}
			if parent := filepath.ToSlash(filepath.Dir(name)); parent != "." {
				if e = mkdirRootParents(dest, parent); e != nil {
					return e
				}
			}
			in, e := source.OpenFile(name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
			if e != nil {
				return e
			}
			info, e := in.Stat()
			if e != nil || !info.Mode().IsRegular() {
				in.Close()
				return errSourcePath
			}
			out, e := dest.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				in.Close()
				return e
			}
			_, e = copySource(ctx, out, in, maxSourceExportBytes)
			in.Close()
			if e == nil {
				e = out.Sync()
			}
			out.Close()
			if e != nil {
				return e
			}
		}
		return nil
	}
	return walk(".", false, 0)
}
func discardRestoredPath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		switch strings.ToLower(part) {
		case "node_modules", ".next", ".next-dev", "dist", "build", "out", "coverage", ".cache", ".turbo", ".vite", ".pnpm-store":
			return true
		}
		if strings.HasPrefix(strings.ToLower(part), ".atoms-") || strings.HasSuffix(strings.ToLower(part), ".tsbuildinfo") {
			return true
		}
	}
	return false
}
func mkdirRootParents(root *os.Root, name string) error {
	parent := ""
	for _, part := range strings.Split(name, "/") {
		if parent != "" {
			parent += "/"
		}
		parent += part
		if err := root.Mkdir(parent, 0750); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

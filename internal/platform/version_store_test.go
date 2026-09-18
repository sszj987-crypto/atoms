package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func versionFixture(t *testing.T) Project {
	t.Helper()
	base := t.TempDir()
	p := Project{ID: newID(), Name: "版本测试项目", WorkspacePath: filepath.Join(base, "workspace"), CodexStatePath: filepath.Join(base, "codex")}
	for _, dir := range []string{p.WorkspacePath, p.CodexStatePath} {
		if err := os.Mkdir(dir, 0750); err != nil {
			t.Fatal(err)
		}
	}
	for name, raw := range map[string]string{"app/page.tsx": "export default function Page() { return '旧版本' }\n", "package.json": "{}", "pnpm-lock.yaml": "lockfileVersion: 9\n", ".env.local": "PRIVATE=keep-current", "public/中文 图片.svg": "<svg/>", "node_modules/a/index.js": "excluded"} {
		writeFixtureSource(t, p, name, raw)
	}
	return p
}
func writeFixtureSource(t *testing.T, p Project, name, raw string) {
	t.Helper()
	target := filepath.Join(p.WorkspacePath, filepath.FromSlash(name))
	if e := os.MkdirAll(filepath.Dir(target), 0750); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(target, []byte(raw), 0640); e != nil {
		t.Fatal(e)
	}
}
func TestVersionSnapshotRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := versionFixture(t)
	id := newID()
	if e := os.Symlink("app/page.tsx", filepath.Join(p.WorkspacePath, "link.ts")); e != nil {
		t.Fatal(e)
	}
	manifest, e := writeVersionSnapshot(ctx, p, id, nil)
	if e != nil {
		t.Fatal(e)
	}
	store, e := openVersionStore(p)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	destPath := t.TempDir()
	dest, e := os.OpenRoot(destPath)
	if e != nil {
		t.Fatal(e)
	}
	defer dest.Close()
	if e = extractVersion(ctx, store, id, manifest.Hash, dest); e != nil {
		t.Fatal(e)
	}
	restored, e := sourceManifest(ctx, destPath)
	if e != nil || restored.Hash != manifest.Hash {
		t.Fatalf("source hash mismatch: %v", e)
	}
	for _, name := range []string{".env.local", "node_modules", "link.ts"} {
		if _, e = dest.Stat(name); !errors.Is(e, os.ErrNotExist) {
			t.Fatalf("excluded entry restored: %s", name)
		}
	}
	raw, e := dest.ReadFile("public/中文 图片.svg")
	if e != nil || string(raw) != "<svg/>" {
		t.Fatalf("Chinese path lost: %v", e)
	}
	source, e := os.OpenRoot(p.WorkspacePath)
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	if e = source.Remove("link.ts"); e != nil {
		t.Fatal(e)
	}
	writeFixtureSource(t, p, "new-dir/.env.local", "SECRET=live")
	writeFixtureSource(t, p, "new-dir/removed.ts", "not retained")
	writeFixtureSource(t, p, ".next/cache/a", "build")
	writeFixtureSource(t, p, ".next-dev/cache/a", "development build")
	if e = preserveWorkspace(ctx, source, dest); e != nil {
		t.Fatal(e)
	}
	raw, e = dest.ReadFile("new-dir/.env.local")
	if e != nil || string(raw) != "SECRET=live" {
		t.Fatalf("private file lost: %v", e)
	}
	if _, e = dest.Stat("new-dir/removed.ts"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("new source retained")
	}
	if _, e = dest.Stat(".next-dev"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("development build retained")
	}
	restored, e = sourceManifest(ctx, destPath)
	if e != nil || restored.Hash != manifest.Hash {
		t.Fatalf("private configuration changed source hash: %v", e)
	}
}
func TestVersionManifestAndZIPValidation(t *testing.T) {
	for _, kind := range []string{"traversal", "symlink", "special", "duplicate", "hash", "size", "missing", "extra", "content-size"} {
		t.Run(kind, func(t *testing.T) {
			p := versionFixture(t)
			id := newID()
			store, e := openVersionStore(p)
			if e != nil {
				t.Fatal(e)
			}
			defer store.Close()
			if e = store.Mkdir(id, 0700); e != nil {
				t.Fatal(e)
			}
			digest := sha256.Sum256([]byte("x"))
			entries := []versionEntry{{Path: "file.ts", Size: 1, Hash: hex.EncodeToString(digest[:]), Mode: 0640}}
			var buf bytes.Buffer
			writer := zip.NewWriter(&buf)
			header := &zip.FileHeader{Name: "file.ts"}
			header.SetMode(0640)
			switch kind {
			case "traversal":
				entries[0].Path = "../outside"
				header.Name = "../outside"
			case "symlink":
				header.SetMode(os.ModeSymlink | 0777)
			case "special":
				header.SetMode(os.ModeNamedPipe | 0600)
			case "duplicate":
				entries = append(entries, entries[0])
			case "size":
				entries[0].Size = maxSourceExportBytes + 1
			case "missing":
				header.Name = "another.ts"
			case "hash":
				wrong := sha256.Sum256([]byte("wrong"))
				entries[0].Hash = hex.EncodeToString(wrong[:])
			}
			file, e := writer.CreateHeader(header)
			if e != nil {
				t.Fatal(e)
			}
			io.WriteString(file, "x")
			if kind == "content-size" {
				io.WriteString(file, "too large")
			}
			if kind == "extra" {
				extra, _ := writer.Create("extra.ts")
				io.WriteString(extra, "extra")
			}
			writer.Close()
			manifest := versionManifest{Entries: entries, Hash: manifestHash(entries)}
			raw, _ := json.Marshal(manifest)
			if e = writeStoreFile(store, id+"/manifest.json", raw); e != nil {
				t.Fatal(e)
			}
			if e = writeStoreFile(store, id+"/source.zip", buf.Bytes()); e != nil {
				t.Fatal(e)
			}
			dest, e := os.OpenRoot(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer dest.Close()
			if e = extractVersion(context.Background(), store, id, manifest.Hash, dest); e == nil {
				t.Fatalf("unsafe %s snapshot accepted", kind)
			}
		})
	}
}
func TestVersionLimitsAndCancellation(t *testing.T) {
	p := versionFixture(t)
	file, e := os.Create(filepath.Join(p.WorkspacePath, "huge.bin"))
	if e != nil {
		t.Fatal(e)
	}
	file.Truncate(maxSourceExportBytes + 1)
	file.Close()
	root, e := os.OpenRoot(p.WorkspacePath)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	if _, e = buildVersion(context.Background(), root, io.Discard); !errors.Is(e, errSourceLimit) {
		t.Fatalf("large snapshot accepted: %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = buildVersion(ctx, root, io.Discard); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", e)
	}
}

func TestVersionManifestFileCountBoundary(t *testing.T) {
	p := versionFixture(t)
	store, err := openVersionStore(p)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	digest := sha256.Sum256(nil)
	for _, count := range []int{maxSourceExportFiles, maxSourceExportFiles + 1} {
		id := newID()
		if err = store.Mkdir(id, 0700); err != nil {
			t.Fatal(err)
		}
		entries := make([]versionEntry, count)
		for i := range entries {
			entries[i] = versionEntry{Path: fmt.Sprintf("%05d.ts", i), Hash: hex.EncodeToString(digest[:]), Mode: 0640}
		}
		manifest := versionManifest{Entries: entries, Hash: manifestHash(entries)}
		raw, _ := json.Marshal(manifest)
		if err = writeStoreFile(store, id+"/manifest.json", raw); err != nil {
			t.Fatal(err)
		}
		_, err = loadVersionManifest(store, id, manifest.Hash)
		if count == maxSourceExportFiles && err != nil {
			t.Fatalf("exact file limit rejected: %v", err)
		}
		if count > maxSourceExportFiles && !errors.Is(err, errVersionCorrupt) {
			t.Fatalf("excessive file count accepted: %v", err)
		}
	}
}

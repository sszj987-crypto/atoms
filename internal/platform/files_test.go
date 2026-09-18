package platform

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func sourceFixture(t *testing.T, files map[string][]byte) (string, *os.Root) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0640); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return dir, root
}

func TestSourcePathPolicy(t *testing.T) {
	if sourcePathAllowed(".next-dev/types/routes.d.ts") {
		t.Fatal("development build must be excluded")
	}
	for _, name := range []string{"app/page.tsx", "pnpm-lock.yaml", "package.json", "public/中文 图片.svg", ".env.example", "config/.env.sample", ".env.template", ".gitignore", "AGENTS.md"} {
		if !sourcePathAllowed(name) {
			t.Errorf("allowed path rejected: %q", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../outside", "/etc/passwd", "app/../page.tsx", "app//page.tsx", "app/", "app\\page.tsx", "C:/secret", "app/line\nfeed", "node_modules/a.ts", "app/node_modules/a.ts", ".next/cache", "dist/index.js", "build/a", "out/a", "coverage/a", ".cache/a", ".git/config", ".codex/auth.json", "codex/auth.json", ".atoms-dependencies.test/a", "logs/run.json", ".env", ".env.local", ".env.production", "app/.env", ".npmrc", "a.log", "key.pem", "key.key", "key.p12", "credentials.json", ".ssh/id_rsa"} {
		if sourcePathAllowed(name) {
			t.Errorf("excluded path accepted: %q", name)
		}
	}
}

func TestListSourceFiltersAndSorts(t *testing.T) {
	dir, root := sourceFixture(t, map[string][]byte{
		"z.ts": []byte("z"), "app/page.tsx": []byte("page"), "public/中文 图片.svg": []byte("svg"),
		".env.example": []byte("EXAMPLE="), ".env.local": []byte("secret"), "node_modules/pkg/a": []byte("dependency"), ".git/config": []byte("secret"), "keys/server.pem": []byte("private"),
	})
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app", filepath.Join(dir, "linked-app")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := listSource(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Path)
		if entry.IsDir && entry.Size != 0 {
			t.Errorf("directory has size: %+v", entry)
		}
	}
	want := []string{".env.example", "app", "app/page.tsx", "empty", "keys", "public", "public/中文 图片.svg", "z.ts"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("entries = %q, want %q", names, want)
	}
}

func TestReadSource(t *testing.T) {
	_, root := sourceFixture(t, map[string][]byte{
		"text.ts": []byte("你好\n\tworld\r\n"), "empty.ts": {}, "binary.png": {0, 1, 2}, "invalid.bin": {0xff},
		"large.ts": bytes.Repeat([]byte("a"), maxSourcePreviewBytes+1), "boundary.ts": bytes.Repeat([]byte("a"), maxSourcePreviewBytes),
	})
	for _, tc := range []struct {
		name, reason string
		previewable  bool
	}{
		{"text.ts", "", true}, {"empty.ts", "", true}, {"binary.png", "binary", false}, {"invalid.bin", "binary", false}, {"large.ts", "too_large", false}, {"boundary.ts", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := readSource(root, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if result.Previewable != tc.previewable || result.Reason != tc.reason {
				t.Fatalf("unexpected metadata: previewable=%v reason=%q", result.Previewable, result.Reason)
			}
			if !result.Previewable && result.Content != "" {
				t.Fatal("non-previewable file returned full content")
			}
		})
	}
	result, _ := readSource(root, "text.ts")
	if result.Content != "你好\n\tworld\r\n" {
		t.Fatalf("content changed: %q", result.Content)
	}
}

func TestOpenSourceRejectsLinksAndSpecialFiles(t *testing.T) {
	dir, root := sourceFixture(t, map[string][]byte{"app/page.tsx": []byte("page"), ".env": []byte("secret")})
	outside := t.TempDir()
	for name, target := range map[string]string{"internal-link.ts": "app/page.tsx", "secret-link.ts": ".env", "external-link": outside, "linked-dir": "app"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "/etc/passwd", "app/../page.tsx", "internal-link.ts", "secret-link.ts", "external-link/file", "linked-dir/page.tsx", "pipe", "app", ".", ".env"} {
		file, err := openSource(root, name, false)
		if err == nil {
			file.Close()
			t.Errorf("unsafe path opened: %q", name)
		}
	}
	if _, err := readSource(root, "missing.ts"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file: %v", err)
	}
}

func TestZipSourceContents(t *testing.T) {
	_, root := sourceFixture(t, map[string][]byte{
		"app/page.tsx": []byte("export default function Page() {}"), "package.json": []byte("{}"), "pnpm-lock.yaml": []byte("lockfileVersion: 9"), "public/中文 图片.png": {0, 1, 2}, ".env.example": []byte("DATABASE_URL="), ".env": []byte("secret"), "node_modules/pkg/index.js": []byte("dependency"), ".next/a": []byte("build"),
	})
	var output bytes.Buffer
	if err := zipSource(context.Background(), root, &output); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(output.Bytes()), int64(output.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[entry.Name] = string(raw)
	}
	want := map[string]string{"app/page.tsx": "export default function Page() {}", "package.json": "{}", "pnpm-lock.yaml": "lockfileVersion: 9", "public/中文 图片.png": string([]byte{0, 1, 2}), ".env.example": "DATABASE_URL="}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ZIP contents = %v, want %v", got, want)
	}
}

func TestSourceLimitsAndCancellation(t *testing.T) {
	dir, root := sourceFixture(t, nil)
	file, err := os.Create(filepath.Join(dir, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxSourceExportBytes + 1); err != nil {
		t.Fatal(err)
	}
	file.Close()
	var output bytes.Buffer
	if err := zipSource(context.Background(), root, &output); !errors.Is(err, errSourceLimit) || output.Len() != 0 {
		t.Fatalf("oversized archive not rejected before writing: %v", err)
	}
	if _, err := copySource(context.Background(), io.Discard, strings.NewReader("12345"), 4); !errors.Is(err, errSourceLimit) {
		t.Fatalf("actual byte limit not enforced: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listSource(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("listing did not honor cancellation: %v", err)
	}
	if _, err := copySource(ctx, io.Discard, strings.NewReader("a"), 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy did not honor cancellation: %v", err)
	}
}

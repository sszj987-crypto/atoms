package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyStarter(t *testing.T) {
	dir := t.TempDir()
	if err := copyStarter(dir); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"package.json", "app/page.tsx", "app/api/health/route.ts", "components/inspector.tsx", "test/db.ts"} {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Fatalf("missing starter file %s: %v", file, err)
		}
	}
}

func TestRuntimeName(t *testing.T) {
	if got := runtimeName("abc"); got != "atoms-project-abc" {
		t.Fatalf("unexpected name %q", got)
	}
}

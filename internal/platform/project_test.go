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
	for _, file := range []string{"package.json", "pnpm-lock.yaml", "app/page.tsx", "app/api/health/route.ts", "components/inspector.tsx", "test/db.ts"} {
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

func TestAvailableProjectPort(t *testing.T) {
	for _, tc := range []struct {
		name string
		used []int
		want int
		ok   bool
	}{
		{"first port", nil, 18000, true},
		{"next free port", []int{18000}, 18001, true},
		{"fills gap", []int{18000, 18002}, 18001, true},
		{"range exhausted", []int{18000, 18001}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := availableProjectPort(18000, 2, tc.used)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("availableProjectPort() = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

package platform

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const fixtureNextDeclaration = `/// <reference types="next" />
/// <reference types="next/image-types/global" />
/// <reference path="./.next-dev/types/routes.d.ts" />

// NOTE: This file should not be edited
// see https://nextjs.org/docs/app/api-reference/config/typescript for more information.
`

func TestRestorePreservesNextGeneratedDeclarations(t *testing.T) {
	for _, original := range []string{fixtureNextDeclaration, "", "declare const custom: string;\n"} {
		t.Run(original, func(t *testing.T) {
			p := versionFixture(t)
			if original != "" {
				writeFixtureSource(t, p, "next-env.d.ts", original)
			}
			before, err := sourceManifest(context.Background(), p.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.ReplaceAll([]byte(fixtureNextDeclaration), []byte(".next-dev"), []byte(".next"))
			err = runPreservingNextDeclaration(p.WorkspacePath, func() error { return os.WriteFile(filepath.Join(p.WorkspacePath, "next-env.d.ts"), changed, 0640) })
			if err != nil {
				t.Fatal(err)
			}
			after, err := sourceManifest(context.Background(), p.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			if original == "declare const custom: string;\n" {
				if before.Hash == after.Hash {
					t.Fatal("custom declarations lost integrity protection")
				}
			} else if before.Hash != after.Hash {
				t.Fatal("Next generated a false source mismatch")
			}
		})
	}
}

func TestRestoreRejectsRealSourceChanges(t *testing.T) {
	for _, path := range []string{"app/page.tsx", "tsconfig.json", "next-env.d.ts"} {
		t.Run(path, func(t *testing.T) {
			p := versionFixture(t)
			writeFixtureSource(t, p, "next-env.d.ts", fixtureNextDeclaration)
			before, err := sourceManifest(context.Background(), p.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			err = runPreservingNextDeclaration(p.WorkspacePath, func() error {
				writeFixtureSource(t, p, "next-env.d.ts", string(bytes.ReplaceAll([]byte(fixtureNextDeclaration), []byte(".next-dev"), []byte(".next"))))
				writeFixtureSource(t, p, path, "unexpected source change")
				return nil
			})
			if path == "next-env.d.ts" && !errors.Is(err, errRestoreSourceChanged) {
				t.Fatalf("custom changes accepted: %v", err)
			}
			after, err := sourceManifest(context.Background(), p.WorkspacePath)
			if err != nil {
				t.Fatal(err)
			}
			if before.Hash == after.Hash {
				t.Fatal("business/config source change was hidden")
			}
		})
	}
}

func TestRestoreGeneratedDeclarationDoesNotFollowLinks(t *testing.T) {
	p := versionFixture(t)
	writeFixtureSource(t, p, "next-env.d.ts", fixtureNextDeclaration)
	outside := filepath.Join(t.TempDir(), "outside.d.ts")
	if err := os.WriteFile(outside, []byte(fixtureNextDeclaration), 0600); err != nil {
		t.Fatal(err)
	}
	err := runPreservingNextDeclaration(p.WorkspacePath, func() error {
		if e := os.Remove(filepath.Join(p.WorkspacePath, "next-env.d.ts")); e != nil {
			return e
		}
		return os.Symlink(outside, filepath.Join(p.WorkspacePath, "next-env.d.ts"))
	})
	if err == nil {
		t.Fatal("followed a replacement link")
	}
	raw, e := os.ReadFile(outside)
	if e != nil || string(raw) != fixtureNextDeclaration {
		t.Fatal("modified external file")
	}
}

func TestRestoreDeclarationPreservesVerificationFailure(t *testing.T) {
	p := versionFixture(t)
	writeFixtureSource(t, p, "next-env.d.ts", fixtureNextDeclaration)
	failed := errors.New("build failed")
	err := runPreservingNextDeclaration(p.WorkspacePath, func() error {
		writeFixtureSource(t, p, "next-env.d.ts", string(bytes.ReplaceAll([]byte(fixtureNextDeclaration), []byte(".next-dev"), []byte(".next"))))
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal("build failure was suppressed")
	}
	raw, e := os.ReadFile(filepath.Join(p.WorkspacePath, "next-env.d.ts"))
	if e != nil || string(raw) != fixtureNextDeclaration {
		t.Fatal("failure left generated source changed")
	}
}

// Opt-in real Next build regression in a disposable fixture container. No Docker
// socket, database, public port or paid model is needed.
func TestRestoreNextDeclarationRealBuild(t *testing.T) {
	if os.Getenv("ATOMS_RESTORE_NEXT_BUILD_FIXTURE") != "1" {
		t.Skip("requires disposable Next fixture container")
	}
	const workspace = "/workspace"
	marker, err := os.ReadFile(filepath.Join(workspace, ".atoms-restore-fixture"))
	if err != nil || string(marker) != "atoms-restore-generated-declaration-fixture" {
		t.Fatal("disposable fixture marker required")
	}
	before, err := sourceManifest(context.Background(), workspace)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	err = runPreservingNextDeclaration(workspace, func() error {
		original, e := os.ReadFile(filepath.Join(workspace, "next-env.d.ts"))
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, "pnpm", "build")
		command.Dir = workspace
		output, e := command.CombinedOutput()
		if e != nil {
			t.Log(string(output))
			return e
		}
		current, e := os.ReadFile(filepath.Join(workspace, "next-env.d.ts"))
		if e != nil {
			return e
		}
		changed = !bytes.Equal(original, current)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("fixture did not reproduce a dev/build declaration rewrite")
	}
	after, err := sourceManifest(context.Background(), workspace)
	if err != nil || after.Hash != before.Hash {
		t.Fatalf("real Next build changed historical source: %v %s != %s", err, after.Hash, before.Hash)
	}
	t.Log("actual Next production build rewrote generated declaration; historical bytes and full source hash restored")
}

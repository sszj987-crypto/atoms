package platform

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// Next rewrites this generated declaration when switching dev/build directories.
// Preserve its historical bytes; never omit it or other source from the hash.
var nextRouteDeclaration = regexp.MustCompile(`^(/// <reference path="\./[^"\r\n]+/types/routes\.d\.ts" />|import "\./[^"\r\n]+/types/routes\.d\.ts";)$`)

func isGeneratedNextDeclaration(raw []byte) bool {
	if len(raw) > 32768 {
		return false
	}
	next, notice := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case "", `/// <reference types="next/image-types/global" />`:
		case `/// <reference types="next" />`:
			next = true
		case "// NOTE: This file should not be edited":
			notice = true
		case "// see https://nextjs.org/docs/app/api-reference/config/typescript for more information.", "// see https://nextjs.org/docs/basic-features/typescript for more information.":
		default:
			if !nextRouteDeclaration.MatchString(line) {
				return false
			}
		}
	}
	return next && notice
}

func readNextDeclaration(root *os.Root) ([]byte, os.FileMode, bool, error) {
	f, err := root.OpenFile("next-env.d.ts", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, 0, false, err
	}
	if !stat.Mode().IsRegular() {
		return nil, 0, false, errSourcePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, 32769))
	return raw, stat.Mode().Perm(), true, err
}

func runPreservingNextDeclaration(workspace string, action func() error) (result error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return err
	}
	defer root.Close()
	original, mode, existed, err := readNextDeclaration(root)
	if err != nil {
		return err
	}
	// Custom declarations are source. A later full hash check must reject changes.
	if existed && !isGeneratedNextDeclaration(original) {
		return action()
	}
	defer func() {
		current, _, present, err := readNextDeclaration(root)
		if err == nil && present && bytes.Equal(current, original) {
			return
		}
		if err == nil && present && !isGeneratedNextDeclaration(current) {
			err = errRestoreSourceChanged
		}
		if err == nil && !existed && present {
			err = root.Remove("next-env.d.ts")
		}
		if err == nil && existed {
			var f *os.File
			f, err = root.OpenFile("next-env.d.ts", os.O_CREATE|os.O_WRONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, mode)
			if err == nil {
				stat, e := f.Stat()
				if e != nil {
					err = e
				} else if !stat.Mode().IsRegular() {
					err = errSourcePath
				}
				if err == nil {
					err = f.Truncate(0)
				}
				if err == nil {
					_, err = f.Write(original)
				}
				if err == nil {
					err = f.Chmod(mode)
				}
				if err == nil {
					err = f.Sync()
				}
				f.Close()
			}
		}
		if err != nil && result == nil {
			result = err
		}
	}()
	return action()
}

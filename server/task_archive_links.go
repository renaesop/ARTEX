package server

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	pgdb "github.com/Autumn-27/artex/db"
)

func collectWorkspaceLinks(staged, original string) (*pgdb.TaskArchiveWorkspaceLinks, error) {
	abs, err := filepath.Abs(original)
	if err != nil {
		return nil, err
	}
	links := &pgdb.TaskArchiveWorkspaceLinks{OriginalRoot: filepath.ToSlash(abs), Targets: make(map[string]string)}
	err = filepath.WalkDir(staged, func(name string, entry fs.DirEntry, err error) error {
		if name == staged && errors.Is(err, fs.ErrNotExist) {
			return nil // Tasks without a workspace are valid.
		}
		if err != nil || entry.Type()&os.ModeSymlink == 0 {
			return err
		}
		if name == staged {
			return fmt.Errorf("task workspace must be a directory: %s", staged)
		}
		relative, err := filepath.Rel(staged, name)
		if err != nil {
			return err
		}
		target, err := os.Readlink(name)
		if err != nil {
			return err
		}
		if !utf8.ValidString(relative) || !utf8.ValidString(target) || len(links.Targets) >= maxArchiveFiles {
			return fmt.Errorf("unrepresentable workspace symlink %q", relative)
		}
		links.Targets[filepath.ToSlash(relative)] = filepath.ToSlash(target)
		return nil
	})
	if err != nil || len(links.Targets) == 0 {
		return nil, err
	}
	return links, nil
}

// workspaceRelativeTarget rebases absolute links to the original workspace so they
// still work under a different data directory. Keep trailing components verbatim:
// cleaning "alias/../x" before expanding alias can change its meaning.
func workspaceRelativeTarget(name, target, originalRoot string) (string, bool) {
	if originalRoot != "" && target == originalRoot {
		target = strings.Repeat("../", strings.Count(name, "/")) + "."
	} else if suffix, ok := strings.CutPrefix(target, originalRoot+"/"); originalRoot != "" && ok {
		target = strings.Repeat("../", strings.Count(name, "/")) + "./" + suffix
	}
	local := filepath.FromSlash(target)
	return target, target != "" && !strings.HasPrefix(target, "/") &&
		filepath.VolumeName(local) == "" && filepath.ToSlash(local) == target && !strings.ContainsRune(target, 0)
}

// Resolve components against the complete manifest before creating any links.
// A lexical Clean or Rel check would miss an escape through "dirlink/../..".
func workspaceLinkStaysInside(name string, links *pgdb.TaskArchiveWorkspaceLinks) bool {
	var dirs []string
	pending := strings.Split(name, "/")
	hops := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
		case "..":
			if len(dirs) == 0 {
				return false
			}
			dirs = dirs[:len(dirs)-1]
		default:
			dirs = append(dirs, part)
			link := strings.Join(dirs, "/")
			if target, ok := links.Targets[link]; ok {
				hops++
				relative, local := workspaceRelativeTarget(link, target, links.OriginalRoot)
				if !local || hops > 40 {
					return false
				}
				dirs = dirs[:len(dirs)-1]
				pending = append(strings.Split(relative, "/"), pending...)
			}
		}
	}
	return true
}

func restoreWorkspaceLinks(workspace string, snapshot *pgdb.TaskArchiveSnapshot) ([]string, error) {
	links := snapshot.WorkspaceLinks
	if links == nil {
		return nil, nil // Older archives contain only the regular files and directories.
	}
	if snapshot.FormatVersion < 4 || len(links.Targets) > maxArchiveFiles {
		return nil, pgdb.ErrTaskArchiveFormatMismatch
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	names := slices.Sorted(maps.Keys(links.Targets))
	// Extraction accepts only regular files and directories. Check every location
	// while that is still true, so no link can become another entry's parent.
	for _, name := range names {
		local, err := filepath.Localize(name)
		if err != nil || name == "." || strings.ContainsRune(name, 0) {
			return nil, fmt.Errorf("invalid workspace link path %q", name)
		}
		if _, err := root.Lstat(local); !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("workspace link path is not free: %q", name)
		}
		parent, err := root.Stat(filepath.Dir(local))
		if err != nil || !parent.IsDir() {
			return nil, fmt.Errorf("missing workspace link parent for %q", name)
		}
	}

	var warnings []string
	var installed []string
	for _, name := range names {
		target := links.Targets[name]
		if !workspaceLinkStaysInside(name, links) {
			warnings = append(warnings, fmt.Sprintf("Workspace symlink %q -> %q was not restored: target leaves the workspace or cannot be resolved safely.", name, target))
			continue
		}
		relative, _ := workspaceRelativeTarget(name, target, links.OriginalRoot)
		if err := root.Symlink(filepath.FromSlash(relative), filepath.FromSlash(name)); err != nil {
			return nil, fmt.Errorf("restore workspace symlink %q: %w", name, err)
		}
		installed = append(installed, name)
	}
	// Also check the filesystem's actual lookup rules (e.g. case-insensitive aliases).
	// Root.Stat cannot traverse outside the workspace. Keep internal dangling links.
	for _, name := range installed {
		if _, err := root.Stat(filepath.FromSlash(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("validate restored workspace symlink %q: %w", name, err)
		}
	}
	return warnings, nil
}

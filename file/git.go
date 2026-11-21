package file

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// GitWalkOptions limits how far WalkGitProjects looks.
type GitWalkOptions struct {
	// MaxDepth is how many directories below the root a project can be, so 1
	// finds only the root's immediate children. Zero means no limit.
	MaxDepth int
	// Skip, if set, is asked about each directory below the root before it's
	// checked for a project, and returning true leaves it and everything
	// under it out.
	Skip func(path string, d fs.DirEntry) bool
}

// WalkGitProjects returns the paths of the git projects at or under root, in
// lexical order. A project is any directory with a .git entry, which is a
// file rather than a directory for worktrees and submodules. Projects aren't
// looked inside, so nested projects and submodules aren't returned, and
// symlinks below root aren't followed.
//
// An error is returned only if root isn't a readable directory; directories
// below it that can't be read are left out.
func WalkGitProjects(root string, opts GitWalkOptions) ([]string, error) {
	// WalkDir won't descend into a symlinked root, so walk where it points and
	// report paths under root as given.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: not a directory", root)
	}

	var projects []string
	err = filepath.WalkDir(
		resolved,
		func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == resolved {
					return err
				}
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(resolved, path)
			if err != nil {
				return err
			}
			path = filepath.Join(root, rel)
			if rel != "." && opts.Skip != nil && opts.Skip(path, d) {
				return fs.SkipDir
			}
			if isGitProject(path) {
				projects = append(projects, path)
				return fs.SkipDir
			}
			if opts.MaxDepth > 0 && depth(rel) >= opts.MaxDepth {
				return fs.SkipDir
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	slices.Sort(projects)
	return projects, nil
}

// SkipHidden is a GitWalkOptions.Skip that leaves out hidden directories.
func SkipHidden(_ string, d fs.DirEntry) bool {
	return strings.HasPrefix(d.Name(), ".")
}

// ErrNotInGitProject is returned by EnclosingGitProject when no directory
// above the path is a git project.
var ErrNotInGitProject = errors.New("not inside a git project")

// EnclosingGitProject returns the root of the git project containing path:
// the nearest of path and its ancestors with a .git entry, as an absolute
// path. Within a worktree or submodule, that's the worktree or submodule.
// path needn't exist.
func EnclosingGitProject(path string) (string, error) {
	dir, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	for {
		if isGitProject(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", ErrNotInGitProject
		}
		dir = parent
	}
}

func isGitProject(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

func depth(rel string) int {
	if rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

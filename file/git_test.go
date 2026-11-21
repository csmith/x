package file

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitTree builds a tree under a temp dir, where entries ending in "/.git/"
// are repository directories, ones ending in "/.git" are worktree-style
// .git files, and anything else ending in "/" is a plain directory.
func gitTree(t *testing.T, entries ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, e := range entries {
		path := filepath.Join(root, filepath.FromSlash(e))
		if e[len(e)-1] == '/' {
			require.NoError(t, os.MkdirAll(path, 0o755))
			continue
		}
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("gitdir: /elsewhere\n"), 0o644))
	}
	return root
}

func joinAll(root string, rels ...string) []string {
	paths := make([]string, len(rels))
	for i, rel := range rels {
		paths[i] = filepath.Join(root, filepath.FromSlash(rel))
	}
	return paths
}

func TestWalkGitProjects_findsDirectoryAndFileRepos(t *testing.T) {
	root := gitTree(
		t,
		"a/.git/",
		"b/worktree/.git",
		"c/plain/",
		"d/file.txt",
	)

	projects, err := WalkGitProjects(root, GitWalkOptions{})

	require.NoError(t, err)
	assert.Equal(t, joinAll(root, "a", "b/worktree"), projects)
}

func TestWalkGitProjects_doesNotDescendIntoProjects(t *testing.T) {
	root := gitTree(
		t,
		"outer/.git/",
		"outer/inner/.git/",
		"outer/sub/module/.git",
		"other/.git/",
	)

	projects, err := WalkGitProjects(root, GitWalkOptions{})

	require.NoError(t, err)
	assert.Equal(t, joinAll(root, "other", "outer"), projects)
}

func TestWalkGitProjects_rootIsProject(t *testing.T) {
	root := gitTree(t, ".git/", "inner/.git/")

	projects, err := WalkGitProjects(root, GitWalkOptions{})

	require.NoError(t, err)
	assert.Equal(t, []string{root}, projects)
}

func TestWalkGitProjects_skip(t *testing.T) {
	root := gitTree(
		t,
		"keep/.git/",
		"node_modules/dep/.git/",
		"deep/node_modules/dep/.git/",
		".hidden/repo/.git/",
	)
	var asked []string

	projects, err := WalkGitProjects(
		root,
		GitWalkOptions{
			Skip: func(path string, d fs.DirEntry) bool {
				asked = append(asked, path)
				return d.Name() == "node_modules"
			},
		},
	)

	require.NoError(t, err)
	assert.Equal(t, joinAll(root, ".hidden/repo", "keep"), projects)
	assert.NotContains(t, asked, root)
	assert.Contains(t, asked, filepath.Join(root, "deep", "node_modules"))
}

func TestWalkGitProjects_skipIsAskedBeforeProjectCheck(t *testing.T) {
	root := gitTree(t, "skipped/.git/", "kept/.git/")

	projects, err := WalkGitProjects(
		root,
		GitWalkOptions{
			Skip: func(_ string, d fs.DirEntry) bool { return d.Name() == "skipped" },
		},
	)

	require.NoError(t, err)
	assert.Equal(t, joinAll(root, "kept"), projects)
}

func TestWalkGitProjects_skipHidden(t *testing.T) {
	root := gitTree(t, "visible/.git/", ".hidden/repo/.git/", "x/.cache/repo/.git/")

	projects, err := WalkGitProjects(root, GitWalkOptions{Skip: SkipHidden})

	require.NoError(t, err)
	assert.Equal(t, joinAll(root, "visible"), projects)
}

func TestWalkGitProjects_maxDepth(t *testing.T) {
	root := gitTree(
		t,
		"one/.git/",
		"a/two/.git/",
		"a/b/three/.git/",
	)

	tests := []struct {
		depth int
		want  []string
	}{
		{1, joinAll(root, "one")},
		{2, joinAll(root, "a/two", "one")},
		{3, joinAll(root, "a/b/three", "a/two", "one")},
		{0, joinAll(root, "a/b/three", "a/two", "one")},
	}
	for _, tt := range tests {
		projects, err := WalkGitProjects(root, GitWalkOptions{MaxDepth: tt.depth})

		require.NoError(t, err)
		assert.Equal(t, tt.want, projects, "MaxDepth %d", tt.depth)
	}
}

func TestWalkGitProjects_noProjects(t *testing.T) {
	projects, err := WalkGitProjects(gitTree(t, "a/b/", "c.txt"), GitWalkOptions{})

	require.NoError(t, err)
	assert.Empty(t, projects)
}

func TestWalkGitProjects_symlinkedRoot(t *testing.T) {
	target := gitTree(t, "proj/.git/")
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))

	projects, err := WalkGitProjects(
		link,
		GitWalkOptions{
			Skip: func(path string, _ fs.DirEntry) bool {
				assert.Equal(t, filepath.Join(link, "proj"), path)
				return false
			},
		},
	)

	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(link, "proj")}, projects)
}

func TestWalkGitProjects_fileRoot(t *testing.T) {
	_, err := WalkGitProjects(filepath.Join(gitTree(t, "file.txt"), "file.txt"), GitWalkOptions{})

	assert.ErrorContains(t, err, "not a directory")
}

func TestWalkGitProjects_sortsResults(t *testing.T) {
	root := gitTree(t, "a/x/.git/", "a-b/.git/", "proj/.git/")

	projects, err := WalkGitProjects(root, GitWalkOptions{})

	require.NoError(t, err)
	assert.Equal(t, joinAll(root, "a-b", "a/x", "proj"), projects)
}

func TestWalkGitProjects_missingRoot(t *testing.T) {
	_, err := WalkGitProjects(filepath.Join(t.TempDir(), "missing"), GitWalkOptions{})

	assert.ErrorIs(t, err, fs.ErrNotExist)
}

func TestEnclosingGitProject_findsNearest(t *testing.T) {
	root := gitTree(
		t,
		"outer/.git/",
		"outer/src/pkg/file.go",
		"outer/inner/.git",
		"outer/inner/deep/",
	)

	tests := []struct {
		path string
		want string
	}{
		{"outer", "outer"},
		{"outer/src/pkg", "outer"},
		{"outer/src/pkg/file.go", "outer"},
		{"outer/src/missing/file.go", "outer"},
		{"outer/inner", "outer/inner"},
		{"outer/inner/deep", "outer/inner"},
	}
	for _, tt := range tests {
		got, err := EnclosingGitProject(filepath.Join(root, filepath.FromSlash(tt.path)))

		require.NoError(t, err, tt.path)
		assert.Equal(t, filepath.Join(root, filepath.FromSlash(tt.want)), got, tt.path)
	}
}

func TestEnclosingGitProject_relativePath(t *testing.T) {
	root := gitTree(t, "repo/.git/", "repo/sub/")
	t.Chdir(filepath.Join(root, "repo", "sub"))

	got, err := EnclosingGitProject(".")

	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "repo"), got)
}

func TestEnclosingGitProject_notFound(t *testing.T) {
	root := gitTree(t, "a/b/")
	if _, err := EnclosingGitProject(root); err == nil {
		t.Skip("temp dir is inside a git project")
	}

	_, err := EnclosingGitProject(filepath.Join(root, "a", "b"))

	assert.ErrorIs(t, err, ErrNotInGitProject)
}

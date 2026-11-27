package text

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDiff(t *testing.T) {
	tests := []struct {
		name         string
		old, updated string
		want         []DiffOp
	}{
		{
			"both empty",
			"",
			"",
			nil,
		},
		{
			"identical",
			"a\nb\n",
			"a\nb\n",
			[]DiffOp{
				{DiffEqual, "a\n", 1, 1},
				{DiffEqual, "b\n", 2, 2},
			},
		},
		{
			"from empty",
			"",
			"a\nb\n",
			[]DiffOp{
				{DiffInsert, "a\n", 1, 1},
				{DiffInsert, "b\n", 1, 2},
			},
		},
		{
			"to empty",
			"a\nb\n",
			"",
			[]DiffOp{
				{DiffDelete, "a\n", 1, 1},
				{DiffDelete, "b\n", 2, 1},
			},
		},
		{
			"pure addition",
			"a\nc\n",
			"a\nb\nc\n",
			[]DiffOp{
				{DiffEqual, "a\n", 1, 1},
				{DiffInsert, "b\n", 2, 2},
				{DiffEqual, "c\n", 2, 3},
			},
		},
		{
			"pure deletion",
			"a\nb\nc\n",
			"a\nc\n",
			[]DiffOp{
				{DiffEqual, "a\n", 1, 1},
				{DiffDelete, "b\n", 2, 2},
				{DiffEqual, "c\n", 3, 2},
			},
		},
		{
			"replacement deletes first",
			"a\nb\nc\n",
			"a\nx\nc\n",
			[]DiffOp{
				{DiffEqual, "a\n", 1, 1},
				{DiffDelete, "b\n", 2, 2},
				{DiffInsert, "x\n", 3, 2},
				{DiffEqual, "c\n", 3, 3},
			},
		},
		{
			"keeps common lines inside a change",
			"a\nb\nc\nd\n",
			"x\nb\ny\nd\n",
			[]DiffOp{
				{DiffDelete, "a\n", 1, 1},
				{DiffInsert, "x\n", 2, 1},
				{DiffEqual, "b\n", 2, 2},
				{DiffDelete, "c\n", 3, 3},
				{DiffInsert, "y\n", 4, 3},
				{DiffEqual, "d\n", 4, 4},
			},
		},
		{
			"missing final newline",
			"a\n",
			"a",
			[]DiffOp{
				{DiffDelete, "a\n", 1, 1},
				{DiffInsert, "a", 2, 1},
			},
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, Diff(tt.old, tt.updated))
			},
		)
	}
}

func TestDiffLinesSizeCap(t *testing.T) {
	// Every line differs except one in the middle, which an LCS would keep.
	build := func(n int, prefix string) []string {
		lines := make([]string, n)
		for i := range lines {
			lines[i] = fmt.Sprintf("%s%d", prefix, i)
		}
		lines[n/2] = "shared"
		return lines
	}
	kinds := func(ops []DiffOp) map[DiffKind]int {
		counts := map[DiffKind]int{}
		for _, op := range ops {
			counts[op.Kind]++
		}
		return counts
	}

	assert.Equal(
		t,
		map[DiffKind]int{DiffEqual: 1, DiffDelete: 99, DiffInsert: 99},
		kinds(DiffLines(build(100, "a"), build(100, "b"))),
	)

	// 4097² cells is just over the cap.
	n := 4097
	large := DiffLines(build(n, "a"), build(n, "b"))
	assert.Equal(t, map[DiffKind]int{DiffDelete: n, DiffInsert: n}, kinds(large))
	assert.Equal(t, DiffDelete, large[n-1].Kind)
	assert.Equal(t, DiffInsert, large[n].Kind)
	assert.Equal(t, n, large[n-1].OldLine)
	assert.Equal(t, n, large[len(large)-1].NewLine)

	// 65536² cells wraps a 32-bit int to zero.
	n = 65536
	assert.Equal(t, map[DiffKind]int{DiffDelete: n, DiffInsert: n}, kinds(DiffLines(build(n, "a"), build(n, "b"))))
}

func TestDiffLinesTrimsCommonEndsBeforeCap(t *testing.T) {
	// The shared prefix and suffix are far larger than the cap allows, but
	// only the middle needs comparing.
	common := make([]string, 5000)
	for i := range common {
		common[i] = fmt.Sprintf("line %d", i)
	}
	var changes []DiffOp
	for _, op := range DiffLines(
		append(append(append([]string{}, common...), "old"), common...),
		append(append(append([]string{}, common...), "new"), common...),
	) {
		if op.Kind != DiffEqual {
			changes = append(changes, op)
		}
	}
	assert.Equal(
		t,
		[]DiffOp{
			{DiffDelete, "old", 5001, 5001},
			{DiffInsert, "new", 5002, 5001},
		},
		changes,
	)
}

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		_, _ = fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()
}

// replaceLines replaces whole lines of text, given as pairs of old and new
// lines without their newlines. An empty new line removes the line.
func replaceLines(text string, pairs ...string) string {
	var b strings.Builder
	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		for i := 0; i < len(pairs); i += 2 {
			if line == pairs[i] {
				line = pairs[i+1]
			}
		}
		if line != "" {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func TestHunks(t *testing.T) {
	lines := func(h Hunk) []string {
		out := make([]string, 0, len(h.Ops))
		for _, op := range h.Ops {
			out = append(out, string(op.Kind)+strings.TrimSuffix(op.Line, "\n"))
		}
		return out
	}

	t.Run(
		"no changes",
		func(t *testing.T) {
			assert.Nil(t, Hunks(Diff(numbered(5), numbered(5)), 3))
			assert.Nil(t, Hunks(nil, 3))
		},
	)

	t.Run(
		"collapses to context",
		func(t *testing.T) {
			hunks := Hunks(Diff(numbered(20), replaceLines(numbered(20), "10", "ten")), 2)
			assert.Len(t, hunks, 1)
			assert.Equal(t, 7, hunks[0].Skipped)
			assert.Equal(t, []string{" 8", " 9", "-10", "+ten", " 11", " 12"}, lines(hunks[0]))
		},
	)

	t.Run(
		"context clipped at edges",
		func(t *testing.T) {
			hunks := Hunks(Diff(numbered(3), replaceLines(numbered(3), "1", "one")), 5)
			assert.Len(t, hunks, 1)
			assert.Equal(t, 0, hunks[0].Skipped)
			assert.Equal(t, []string{"-1", "+one", " 2", " 3"}, lines(hunks[0]))
		},
	)

	t.Run(
		"nearby changes merge",
		func(t *testing.T) {
			// Four unchanged lines between changes fit in two lots of context.
			hunks := Hunks(Diff(numbered(12), replaceLines(numbered(12), "3", "three", "8", "eight")), 2)
			assert.Len(t, hunks, 1)
			assert.Equal(t, 0, hunks[0].Skipped)
			assert.Equal(
				t,
				[]string{" 1", " 2", "-3", "+three", " 4", " 5", " 6", " 7", "-8", "+eight", " 9", " 10"},
				lines(hunks[0]),
			)
		},
	)

	t.Run(
		"distant changes split",
		func(t *testing.T) {
			// Five unchanged lines between changes is one more than context covers.
			hunks := Hunks(Diff(numbered(14), replaceLines(numbered(14), "3", "three", "9", "nine")), 2)
			assert.Len(t, hunks, 2)
			assert.Equal(t, 0, hunks[0].Skipped)
			assert.Equal(t, []string{" 1", " 2", "-3", "+three", " 4", " 5"}, lines(hunks[0]))
			assert.Equal(t, 1, hunks[1].Skipped)
			assert.Equal(t, []string{" 7", " 8", "-9", "+nine", " 10", " 11"}, lines(hunks[1]))
		},
	)

	t.Run(
		"zero context",
		func(t *testing.T) {
			hunks := Hunks(Diff(numbered(5), replaceLines(numbered(5), "2", "two", "4", "four")), 0)
			assert.Len(t, hunks, 2)
			assert.Equal(t, 1, hunks[0].Skipped)
			assert.Equal(t, []string{"-2", "+two"}, lines(hunks[0]))
			assert.Equal(t, 1, hunks[1].Skipped)
			assert.Equal(t, []string{"-4", "+four"}, lines(hunks[1]))
		},
	)
}

func TestUnifiedDiff_wholeFileContext(t *testing.T) {
	assert.Equal(t, "@@ -1,3 +1,3 @@\n a\n-b\n+x\n c\n", UnifiedDiff("a\nb\nc\n", "a\nx\nc\n", math.MaxInt))
}

func TestUnifiedDiff(t *testing.T) {
	tests := []struct {
		name         string
		old, updated string
		want         string
	}{
		{
			"identical",
			"a\nb\n",
			"a\nb\n",
			"",
		},
		{
			"both empty",
			"",
			"",
			"",
		},
		{
			"from empty",
			"",
			"a\nb\n",
			"@@ -0,0 +1,2 @@\n+a\n+b\n",
		},
		{
			"to empty",
			"a\n",
			"",
			"@@ -1 +0,0 @@\n-a\n",
		},
		{
			"addition",
			numbered(10), replaceLines(numbered(10), "5", "5\nnew"),
			"@@ -3,6 +3,7 @@\n 3\n 4\n 5\n+new\n 6\n 7\n 8\n",
		},
		{
			"deletion",
			numbered(10), replaceLines(numbered(10), "5", ""),
			"@@ -2,7 +2,6 @@\n 2\n 3\n 4\n-5\n 6\n 7\n 8\n",
		},
		{
			"two hunks",
			numbered(20), replaceLines(numbered(20), "2", "two", "18", "eighteen"),
			"@@ -1,5 +1,5 @@\n 1\n-2\n+two\n 3\n 4\n 5\n" +
				"@@ -15,6 +15,6 @@\n 15\n 16\n 17\n-18\n+eighteen\n 19\n 20\n",
		},
		{
			"no newline at end",
			"a\nb\n", "a\nb",
			"@@ -1,2 +1,2 @@\n a\n-b\n+b\n\\ No newline at end of file\n",
		},
	}
	for _, tt := range tests {
		t.Run(
			tt.name,
			func(t *testing.T) {
				assert.Equal(t, tt.want, UnifiedDiff(tt.old, tt.updated, 3))
			},
		)
	}
}

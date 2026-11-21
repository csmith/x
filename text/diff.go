// Package text works with plain text, such as diffing it line by line.
package text

import (
	"fmt"
	"strconv"
	"strings"
)

// maxCells caps the LCS table; larger changes are reported as a wholesale
// replacement.
const maxCells = 16 << 20

// DiffKind is what a DiffOp does to a line.
type DiffKind byte

const (
	DiffEqual  DiffKind = ' '
	DiffDelete DiffKind = '-'
	DiffInsert DiffKind = '+'
)

// DiffOp is one line of a diff.
type DiffOp struct {
	Kind DiffKind
	Line string
	// OldLine and NewLine are the 1-based positions the line has, or would
	// have, in each input.
	OldLine, NewLine int
}

// Diff compares old and updated line by line, each keeping its trailing
// newline. A final line without one differs from the same line with one.
func Diff(old, updated string) []DiffOp {
	return DiffLines(splitLines(old), splitLines(updated))
}

// DiffLines compares two sequences of lines, giving a shortest edit script with
// deletions before insertions within each change. If the lines between the
// common prefix and suffix are too many to compare, they are reported as all
// deleted and then all inserted.
func DiffLines(a, b []string) []DiffOp {
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	kinds := repeat(DiffEqual, prefix)
	kinds = append(kinds, lcsKinds(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])...)
	kinds = append(kinds, repeat(DiffEqual, suffix)...)
	if len(kinds) == 0 {
		return nil
	}

	ops := make([]DiffOp, len(kinds))
	i, j := 0, 0
	for n, k := range kinds {
		ops[n] = DiffOp{Kind: k, OldLine: i + 1, NewLine: j + 1}
		switch k {
		case DiffEqual:
			ops[n].Line = a[i]
			i++
			j++
		case DiffDelete:
			ops[n].Line = a[i]
			i++
		default:
			ops[n].Line = b[j]
			j++
		}
	}
	return ops
}

// Hunk is a run of changes with the unchanged lines around them.
type Hunk struct {
	// Skipped is how many unchanged lines come between the previous hunk (or
	// the start) and this one.
	Skipped int
	Ops     []DiffOp
}

// Hunks groups ops into hunks, keeping up to context unchanged lines either
// side of each change and dropping the rest. Changes separated by no more
// than 2*context unchanged lines share a hunk. It returns nil if nothing
// changed.
func Hunks(ops []DiffOp, context int) []Hunk {
	context = min(max(context, 0), len(ops))
	var hunks []Hunk
	for start := 0; start < len(ops); {
		first := nextChange(ops, start)
		if first < 0 {
			break
		}
		begin := max(first-context, start)
		end := first
		for {
			// end is the last change in this hunk.
			for end+1 < len(ops) && ops[end+1].Kind != DiffEqual {
				end++
			}
			next := nextChange(ops, end+1)
			if next < 0 || next-end-1 > 2*context {
				break
			}
			end = next
		}
		stop := min(end+context+1, len(ops))
		hunks = append(hunks, Hunk{Skipped: begin - start, Ops: ops[begin:stop]})
		start = stop
	}
	return hunks
}

func nextChange(ops []DiffOp, from int) int {
	for i := from; i < len(ops); i++ {
		if ops[i].Kind != DiffEqual {
			return i
		}
	}
	return -1
}

// UnifiedDiff returns the differences between old and updated as unified diff
// hunks with the given lines of context, without file headers, or "" if they
// are equal.
func UnifiedDiff(old, updated string, context int) string {
	if old == updated {
		return ""
	}
	var out strings.Builder
	for _, h := range Hunks(Diff(old, updated), context) {
		writeHunk(&out, h.Ops)
	}
	return out.String()
}

func writeHunk(out *strings.Builder, ops []DiffOp) {
	oldCount, newCount := 0, 0
	for _, op := range ops {
		if op.Kind != DiffInsert {
			oldCount++
		}
		if op.Kind != DiffDelete {
			newCount++
		}
	}
	oldStart, newStart := ops[0].OldLine, ops[0].NewLine
	if oldCount == 0 {
		oldStart--
	}
	if newCount == 0 {
		newStart--
	}
	_, _ = fmt.Fprintf(out, "@@ -%s +%s @@\n", hunkRange(oldStart, oldCount), hunkRange(newStart, newCount))
	for _, op := range ops {
		out.WriteByte(byte(op.Kind))
		out.WriteString(op.Line)
		if !strings.HasSuffix(op.Line, "\n") {
			out.WriteString("\n\\ No newline at end of file\n")
		}
	}
}

func hunkRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}

func splitLines(s string) []string {
	var lines []string
	for line := range strings.Lines(s) {
		lines = append(lines, line)
	}
	return lines
}

// lcsKinds returns the edit script turning a into b, preferring deletions
// before insertions.
func lcsKinds(a, b []string) []DiffKind {
	n, m := len(a), len(b)
	if n == 0 || m == 0 || n > maxCells/m {
		return append(repeat(DiffDelete, n), repeat(DiffInsert, m)...)
	}

	// lcs[i*width+j] is the length of the LCS of a[i:] and b[j:].
	width := m + 1
	lcs := make([]int32, (n+1)*width)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
			} else {
				lcs[i*width+j] = max(lcs[(i+1)*width+j], lcs[i*width+j+1])
			}
		}
	}

	kinds := make([]DiffKind, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			kinds = append(kinds, DiffEqual)
			i++
			j++
		case lcs[(i+1)*width+j] >= lcs[i*width+j+1]:
			kinds = append(kinds, DiffDelete)
			i++
		default:
			kinds = append(kinds, DiffInsert)
			j++
		}
	}
	kinds = append(kinds, repeat(DiffDelete, n-i)...)
	return append(kinds, repeat(DiffInsert, m-j)...)
}

func repeat(k DiffKind, n int) []DiffKind {
	kinds := make([]DiffKind, n)
	for i := range kinds {
		kinds[i] = k
	}
	return kinds
}

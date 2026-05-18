package git

import (
	"fmt"
	"strings"
)

// Hunk is one parsed @@ block from a unified diff, scoped to a single file.
// Body is the verbatim text of the hunk (header line included, no trailing
// newline beyond the hunk's own content) so it can be re-emitted as a
// standalone patch when assembling per-hunk commits.
type Hunk struct {
	Index     int    `json:"index"`      // 0-based position in the parent diff
	File      string `json:"file"`       // post-image path (b/<path>)
	OldFile   string `json:"old_file"`   // pre-image path (a/<path>); same as File except for renames
	OldStart  int    `json:"old_start"`  // 1-based old-file starting line
	OldCount  int    `json:"old_count"`  // number of old-file lines covered
	NewStart  int    `json:"new_start"`  // 1-based new-file starting line
	NewCount  int    `json:"new_count"`  // number of new-file lines covered
	FileHead  string `json:"file_head"`  // diff/index/+++/--- preamble for File (verbatim)
	Body      string `json:"body"`       // @@ header + diff body for this hunk
}

// ParseHunks splits a unified diff into individual Hunks. The diff is the
// output of `git diff ...`. Returned hunks are in source order with monotone
// Index. Returns an empty slice and nil error for the empty/no-diff case.
func ParseHunks(diff string) ([]Hunk, error) {
	if strings.TrimSpace(diff) == "" {
		return nil, nil
	}
	var (
		out         []Hunk
		fileHead    strings.Builder
		curFile     string
		curOldFile  string
		inFileHead  bool
		curHunkHdr  string
		curHunkBody strings.Builder
		curHunk     *Hunk
		idx         int
	)
	flush := func() {
		if curHunk != nil {
			curHunk.Body = curHunkHdr + "\n" + strings.TrimRight(curHunkBody.String(), "\n") + "\n"
			out = append(out, *curHunk)
			curHunk = nil
			curHunkHdr = ""
			curHunkBody.Reset()
		}
	}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			fileHead.Reset()
			fileHead.WriteString(line)
			fileHead.WriteByte('\n')
			inFileHead = true
			parts := strings.Fields(line)
			curFile = ""
			curOldFile = ""
			if len(parts) >= 4 {
				curOldFile = strings.TrimPrefix(parts[2], "a/")
				curFile = strings.TrimPrefix(parts[3], "b/")
			}
		case inFileHead && (strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "new file mode") ||
			strings.HasPrefix(line, "deleted file mode") ||
			strings.HasPrefix(line, "old mode") ||
			strings.HasPrefix(line, "new mode") ||
			strings.HasPrefix(line, "similarity ") ||
			strings.HasPrefix(line, "rename from") ||
			strings.HasPrefix(line, "rename to") ||
			strings.HasPrefix(line, "copy from") ||
			strings.HasPrefix(line, "copy to") ||
			strings.HasPrefix(line, "Binary files") ||
			strings.HasPrefix(line, "GIT binary patch") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ")):
			fileHead.WriteString(line)
			fileHead.WriteByte('\n')
		case strings.HasPrefix(line, "@@"):
			flush()
			inFileHead = false
			os, oc, ns, nc, ok := parseAtAt(line)
			if !ok {
				return nil, fmt.Errorf("malformed hunk header: %q", line)
			}
			curHunk = &Hunk{
				Index:    idx,
				File:     curFile,
				OldFile:  curOldFile,
				OldStart: os,
				OldCount: oc,
				NewStart: ns,
				NewCount: nc,
				FileHead: fileHead.String(),
			}
			curHunkHdr = line
			idx++
		default:
			if curHunk != nil {
				curHunkBody.WriteString(line)
				curHunkBody.WriteByte('\n')
			} else if inFileHead {
				// extra metadata in file head we didn't recognize
				fileHead.WriteString(line)
				fileHead.WriteByte('\n')
			}
		}
	}
	flush()
	return out, nil
}

// parseAtAt parses `@@ -OLD[,c] +NEW[,c] @@ ...` into the four numbers.
// Defaults count to 1 when omitted (per unified-diff spec).
func parseAtAt(s string) (oldStart, oldCount, newStart, newCount int, ok bool) {
	// Find the segment between "@@ " and " @@".
	if !strings.HasPrefix(s, "@@") {
		return 0, 0, 0, 0, false
	}
	rest := strings.TrimPrefix(s, "@@")
	rest = strings.TrimLeft(rest, " ")
	// rest now starts with "-OLD..."
	end := strings.Index(rest, "@@")
	if end < 0 {
		return 0, 0, 0, 0, false
	}
	mid := strings.TrimSpace(rest[:end])
	// Split into the two range tokens.
	tokens := strings.Fields(mid)
	if len(tokens) < 2 {
		return 0, 0, 0, 0, false
	}
	parseRange := func(tok string) (start, count int, ok bool) {
		tok = strings.TrimLeft(tok, "-+")
		if c := strings.IndexByte(tok, ','); c >= 0 {
			s := atoiSimple(tok[:c])
			cnt := atoiSimple(tok[c+1:])
			return s, cnt, true
		}
		return atoiSimple(tok), 1, true
	}
	os, oc, ok1 := parseRange(tokens[0])
	ns, nc, ok2 := parseRange(tokens[1])
	if !ok1 || !ok2 {
		return 0, 0, 0, 0, false
	}
	return os, oc, ns, nc, true
}

func atoiSimple(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// BuildPatch produces a complete patch that `git apply` can consume from the
// supplied hunks. Hunks are grouped per file; each file block is prefixed by
// the saved FileHead so renames / mode changes / binary markers survive. If
// hunks for the same file appear out of order, they are sorted by NewStart
// before emission.
func BuildPatch(hunks []Hunk) string {
	if len(hunks) == 0 {
		return ""
	}
	// Group by file, preserving first-appearance order.
	type group struct {
		head  string
		items []Hunk
	}
	groups := make(map[string]*group)
	var order []string
	for _, h := range hunks {
		g, ok := groups[h.File]
		if !ok {
			g = &group{head: h.FileHead}
			groups[h.File] = g
			order = append(order, h.File)
		}
		g.items = append(g.items, h)
	}
	var b strings.Builder
	for _, f := range order {
		g := groups[f]
		// Sort hunks by NewStart so the patch is monotone (insertion sort).
		hs := g.items
		for i := 1; i < len(hs); i++ {
			for j := i; j > 0 && hs[j-1].NewStart > hs[j].NewStart; j-- {
				hs[j-1], hs[j] = hs[j], hs[j-1]
			}
		}
		b.WriteString(g.head)
		for _, h := range hs {
			b.WriteString(h.Body)
		}
	}
	return b.String()
}

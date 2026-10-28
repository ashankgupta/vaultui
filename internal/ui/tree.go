package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/charmbracelet/x/ansi"

	"vaultui/internal/vault"
)

type node struct {
	name     string // display name: "secret/", "payments/", "db"
	mount    string // "secret/"
	path     string // relative to mount; "" for the mount itself, folders end in "/"
	isDir    bool
	expanded bool
	loaded   bool
	locked   bool // 403
	loading  bool
	kv       int // KV version, mounts only
	children []*node
	parent   *node
	depth    int
}

func (n *node) full() string { return n.mount + n.path }

type tree struct {
	roots   []*node
	visible []*node
	cursor  int
	offset  int

	search     *textinput.Model // non-nil while typing a / query
	query      string
	searchFrom int
}

func newTree(mounts []vault.Mount) tree {
	t := tree{}
	for _, m := range mounts {
		t.roots = append(t.roots, &node{name: m.Path, mount: m.Path, isDir: true, kv: m.Version})
	}
	t.rebuild()
	return t
}

func (t *tree) rebuild() {
	t.visible = t.visible[:0]
	var walk func([]*node)
	walk = func(ns []*node) {
		for _, n := range ns {
			t.visible = append(t.visible, n)
			if n.expanded {
				walk(n.children)
			}
		}
	}
	walk(t.roots)
	t.cursor = clamp(t.cursor, 0, len(t.visible)-1)
}

func (t *tree) selected() *node {
	if t.cursor < 0 || t.cursor >= len(t.visible) {
		return nil
	}
	return t.visible[t.cursor]
}

func (t *tree) selectNode(n *node) {
	for i, v := range t.visible {
		if v == n {
			t.cursor = i
		}
	}
}

func (n *node) setChildren(es []vault.Entry) {
	n.children = n.children[:0]
	for _, e := range es {
		n.children = append(n.children, &node{
			name: e.Name, mount: n.mount, path: n.path + e.Name,
			isDir: e.IsDir, parent: n, depth: n.depth + 1,
		})
	}
	n.loaded = true
}

// deepest returns the loaded node closest to full (a "mount/a/b" path) and
// whether it is an exact match.
func (t *tree) deepest(full string) (*node, bool) {
	var best *node
	ns := t.roots
	for {
		var next *node
		for _, n := range ns {
			if full == n.full() {
				return n, true
			}
			if n.isDir && strings.HasPrefix(full, n.full()) {
				next = n
			}
		}
		if next == nil {
			return best, false
		}
		best, ns = next, next.children
	}
}

func (t *tree) view(w, h int, openPath string, isFav func(string) bool, focused bool, spin string) string {
	t.offset = scroll(t.cursor, t.offset, h)
	guide := stDim.Faint(true).Render("│ ")
	var lines []string
	for i := t.offset; i < len(t.visible) && i < t.offset+h; i++ {
		n := t.visible[i]
		hl := matchAt(n.name, t.query)
		var icon, name string
		switch {
		case n.locked:
			icon, name = stDim.Render("▪ "), stDim.Render(n.name)+" 🔒"
		case n.isDir && n.depth == 0:
			icon = stKeyCap.Render("▸ ")
			if n.expanded {
				icon = stKeyCap.Render("▾ ")
			}
			name = highlightIn(n.name, hl, stTitle) + " " + stDim.Render(fmt.Sprintf("kv%d", n.kv))
		case n.isDir:
			icon = stKeyCap.Render("▸ ")
			if n.expanded {
				icon = stKeyCap.Render("▾ ")
			}
			name = highlightIn(n.name, hl, stFolder)
		case n.full() == openPath:
			icon, name = stValue.Render("◆ "), highlightIn(n.name, hl, stValue.Bold(true))
		default:
			icon, name = stDim.Render("◇ "), highlightIn(n.name, hl, stMuted)
		}
		if n.loading {
			name += " " + spin
		}
		if !n.isDir && isFav(n.full()) {
			name += " " + stStar.Render("★")
		}
		line := strings.Repeat(guide, n.depth) + icon + name
		line = ansi.Truncate(line, w, "…")
		if i == t.cursor {
			if focused {
				line = stSel.Render(pad(ansi.Strip(line), w))
			} else {
				line = lipgloss.NewStyle().Background(cSurface).Render(pad(ansi.Strip(line), w))
			}
		}
		lines = append(lines, line)
	}
	if len(t.visible) == 0 {
		lines = append(lines, stDim.Render(spin+" loading mounts…"))
	}
	return strings.Join(lines, "\n")
}

// scroll keeps cursor within [offset, offset+h).
func scroll(cursor, offset, h int) int {
	if h <= 0 {
		return 0
	}
	if cursor < offset {
		return cursor
	}
	if cursor >= offset+h {
		return cursor - h + 1
	}
	return offset
}

func clamp(v, lo, hi int) int {
	if v > hi {
		v = hi
	}
	if v < lo {
		v = lo
	}
	return v
}

func pad(s string, w int) string {
	if n := ansi.StringWidth(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// --- / search inside the tree (vim style: jump to matches, n/N cycle) ---

// matchAt returns the byte offsets of query in name (case-insensitive), or nil.
func matchAt(name, query string) []int {
	if query == "" {
		return nil
	}
	i := strings.Index(strings.ToLower(name), strings.ToLower(query))
	if i < 0 {
		return nil
	}
	idx := make([]int, 0, len(query))
	for j := range name[i : i+len(query)] {
		idx = append(idx, i+j)
	}
	return idx
}

// matches counts visible nodes matching the query.
func (t *tree) matches() int {
	n := 0
	for _, v := range t.visible {
		if matchAt(v.name, t.query) != nil {
			n++
		}
	}
	return n
}

// findFrom moves the cursor to the next (dir=1) or previous (dir=-1) match,
// starting at from (inclusive) and wrapping. Returns false if none match.
func (t *tree) findFrom(from, dir int) bool {
	n := len(t.visible)
	for i := 0; i < n; i++ {
		j := ((from+dir*i)%n + n) % n
		if matchAt(t.visible[j].name, t.query) != nil {
			t.cursor = j
			return true
		}
	}
	return false
}

func (t *tree) startSearch() tea.Cmd {
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "find in tree"
	in.Focus()
	t.search, t.searchFrom, t.query = &in, t.cursor, ""
	return textinput.Blink
}

func (t *tree) searchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		t.search, t.query, t.cursor = nil, "", t.searchFrom
		return nil
	case "enter":
		t.search = nil // keep the query for n/N and highlighting
		return nil
	case "ctrl+n", "ctrl+j", "down":
		t.findFrom(t.cursor+1, 1)
		return nil
	case "ctrl+p", "ctrl+k", "up":
		t.findFrom(t.cursor-1, -1)
		return nil
	}
	var cmd tea.Cmd
	*t.search, cmd = t.search.Update(msg)
	if q := t.search.Value(); q != t.query {
		t.query = q
		if !t.findFrom(t.searchFrom, 1) {
			t.cursor = t.searchFrom
		}
	}
	return cmd
}

// title is the EXPLORER line, or the search bar while searching.
func (t *tree) title(w, indexed int) string {
	status := func() string {
		switch n := t.matches(); {
		case t.query == "":
			return ""
		case n == 1:
			return stDim.Render("1 match")
		case n > 1:
			return stDim.Render(fmt.Sprintf("%d matches · n/N", n))
		}
		return stErr.Render("no match") + stDim.Render(" · ctrl+p searches all")
	}
	switch {
	case t.search != nil:
		t.search.Width = max(w-30, 6)
		return ansi.Truncate(t.search.View()+" "+status(), w, "…")
	case t.query != "":
		return ansi.Truncate(stKeyCap.Render("/"+t.query)+" "+status()+stDim.Render(" · esc clear"), w, "…")
	}
	s := stTitle.Render("Explorer")
	if indexed > 0 {
		s += " " + stDim.Render(thousands(indexed)+" secrets")
	}
	return s
}

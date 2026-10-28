package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"

	"vaultui/internal/vault"
)

type secretPane struct {
	path      string // requested full path
	sec       *vault.Secret
	all       []string // every key, sorted
	keys      []string // keys shown (filtered by the in-secret search)
	filter    string
	search    *textinput.Model // non-nil while typing a / filter
	match     map[string][]int // matched char positions, for highlighting
	cursor    int
	offset    int
	revealed  map[string]bool
	revealSeq int
	locked    bool
	err       error
}

func newSecretPane(path string) secretPane {
	return secretPane{path: path, revealed: map[string]bool{}}
}

func (p *secretPane) set(s *vault.Secret) {
	p.sec = s
	p.all = sortedKeys(s.Data)
	p.applyFilter()
}

// applyFilter fuzzy-matches key names (never values) against p.filter,
// best match first.
func (p *secretPane) applyFilter() {
	p.cursor, p.offset, p.match = 0, 0, nil
	if p.filter == "" {
		p.keys = p.all
		return
	}
	p.keys, p.match = nil, map[string][]int{}
	for _, r := range fuzzy.Find(p.filter, p.all) {
		p.keys = append(p.keys, r.Str)
		p.match[r.Str] = r.MatchedIndexes
	}
}

func (p *secretPane) startSearch() tea.Cmd {
	in := textinput.New()
	in.Prompt = "/"
	in.Placeholder = "filter keys"
	in.SetValue(p.filter)
	in.Focus()
	p.search = &in
	return textinput.Blink
}

// searchKey handles keys while the / filter is being typed.
func (p *secretPane) searchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		p.search, p.filter = nil, ""
		p.applyFilter()
		return nil
	case "enter":
		p.search = nil // keep the filter; esc clears it later
		return nil
	case "up", "ctrl+k", "ctrl+p":
		p.cursor = max(p.cursor-1, 0)
		return nil
	case "down", "ctrl+j", "ctrl+n":
		p.cursor = min(p.cursor+1, max(len(p.keys)-1, 0))
		return nil
	}
	var cmd tea.Cmd
	*p.search, cmd = p.search.Update(msg)
	if v := p.search.Value(); v != p.filter {
		p.filter = v
		p.applyFilter()
	}
	return cmd
}

func (p *secretPane) selectedKey() string {
	if p.cursor < len(p.keys) {
		return p.keys[p.cursor]
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// oneLine makes a value table-safe.
func oneLine(v string) string { return strings.ReplaceAll(v, "\n", "⏎") }

// breadcrumb renders "secret / payments / prod / db" with the leaf bold.
func breadcrumb(path string) string {
	segs := strings.Split(strings.TrimSuffix(path, "/"), "/")
	for i := range segs[:len(segs)-1] {
		segs[i] = stMuted.Render(segs[i])
	}
	segs[len(segs)-1] = stTitle.Render(segs[len(segs)-1])
	return strings.Join(segs, stDim.Render(" / "))
}

// Rows inside the secret panel; secretTop in app.go depends on these.
const secretHeaderRows = 4 // headline, filter bar, column header, rule

func (p *secretPane) view(w, h int, focused, fav bool) string {
	leaf := p.path[strings.LastIndex(strings.TrimSuffix(p.path, "/"), "/")+1:]
	head := stTitle.Render(leaf)
	if fav {
		head += " " + stStar.Render("★")
	}
	switch {
	case p.locked:
		return head + "\n\n" + stMuted.Render("🔒 Your token's policy doesn't allow reading this secret.")
	case p.err != nil:
		return head + "\n\n" + stErr.Render("✗ "+p.err.Error())
	case p.sec == nil:
		return head + "\n\n" + stDim.Render("loading…")
	}
	s := p.sec
	n := len(p.keys)

	pills := []string{pill(fmt.Sprintf("KV v%d", s.KVVersion), nil)}
	if s.KVVersion == 2 {
		pills = append(pills, pill(fmt.Sprintf("version %d", s.Version), cAccent), pill("updated "+ago(s.Created), nil))
		if s.Destroyed {
			pills = append(pills, pill("destroyed", cRed))
		} else if s.Deleted {
			pills = append(pills, pill("deleted", cRed))
		}
	}
	pills = append(pills, pill(plural(len(p.all), "key"), nil))
	revealed := 0
	for _, on := range p.revealed {
		if on {
			revealed++
		}
	}
	if revealed > 0 {
		pills = append(pills, pill(fmt.Sprintf("%d revealed", revealed), cYellow))
	}
	lines := []string{ansi.Truncate(head+"   "+strings.Join(pills, " "), w, "")}

	switch {
	case p.search != nil:
		p.search.Width = max(w-24, 10)
		lines = append(lines, p.search.View()+"  "+stDim.Render(fmt.Sprintf("%d of %d", n, len(p.all))))
	case p.filter != "":
		lines = append(lines, stWarnBold.Render("/"+p.filter)+"  "+stDim.Render(fmt.Sprintf("%d of %d · / edit · esc clear", n, len(p.all))))
	default:
		lines = append(lines, "")
	}

	kw := 3
	for _, k := range p.keys {
		kw = max(kw, min(ansi.StringWidth(k), 32))
	}
	lines = append(lines,
		stDim.Bold(true).Render(fmt.Sprintf("  %-*s   %s", kw, "KEY", "VALUE")),
		stDim.Faint(true).Render(strings.Repeat("─", w)))

	detail := n > 0 && h >= 14 // room for the selected-key strip at the bottom
	rows := h - len(lines) - 1
	if detail {
		rows -= 4
	}
	p.offset = scroll(p.cursor, p.offset, rows)
	switch {
	case n == 0 && p.filter != "":
		lines = append(lines, stDim.Render("  no keys match /"+p.filter))
	case n == 0:
		lines = append(lines, stDim.Render("  (no data in this version)"))
	}
	for i := p.offset; i < n && i < p.offset+rows; i++ {
		k := p.keys[i]
		v, vs := mask, stDim
		if p.revealed[k] {
			v, vs = oneLine(s.Data[k]), stValue
		}
		key := pad(ansi.Truncate(highlightIn(k, p.match[k], stText), kw, "…"), kw)
		line := "  " + key + "   " + vs.Render(v)
		if i == p.cursor && focused {
			line = lipgloss.NewStyle().Foreground(cAccent).Background(cSelBg).Render("▌ ") +
				stSel.Render(pad(ansi.Truncate(pad(ansi.Strip(key), kw)+"   "+v, w-2, "…"), w-2))
		} else if i == p.cursor {
			line = lipgloss.NewStyle().Background(cSurface).Render(pad(ansi.Truncate(ansi.Strip(line), w, "…"), w))
		}
		lines = append(lines, ansi.Truncate(line, w, "…"))
	}
	if n > rows {
		lines = append(lines, stDim.Render(fmt.Sprintf("  %d-%d of %d keys", p.offset+1, min(p.offset+rows, n), n)))
	}
	if detail {
		for len(lines) < h-3 {
			lines = append(lines, "")
		}
		k := p.selectedKey()
		v := s.Data[k]
		kind, _, _ := describe(v)
		state := pill("masked", nil)
		if p.revealed[k] {
			state = pill("revealed", cYellow)
		}
		act := func(key, d string) string { return stKeyCap.Render(key) + " " + stDim.Render(d) }
		lines = append(lines[:h-3],
			stDim.Faint(true).Render(strings.Repeat("─", w)),
			stTitle.Render(k)+"  "+pill(kind, cAccent2)+" "+pill(plural(len([]rune(v)), "char"), nil)+" "+state,
			act("enter", "inspect")+"   "+act("r", "reveal")+"   "+act("c", "copy")+"   "+act("x", "copy as shell/k8s/docker")+"   "+act("y", "copy path"))
	}
	return strings.Join(lines, "\n")
}

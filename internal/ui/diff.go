package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/sergi/go-diff/diffmatchpatch"

	"vaultui/internal/diff"
	"vaultui/internal/vault"
)

type diffMsg struct {
	title       string
	left, right *vault.Secret
	err         error
}

type diffModal struct {
	title    string
	rows     []diff.Row
	shown    []diff.Row
	showSame bool
	cursor   int
	offset   int
	revealed map[string]bool
}

// openDiffPicker asks for the other path, pre-filled with env suggestions.
func (m *Model) openDiffPicker() tea.Cmd {
	cur := m.sec.path
	if m.sec.sec == nil {
		return nil
	}
	sugg := diff.EnvSuggestions(cur, m.Config.Environments)
	if len(m.indexPaths) > 0 { // only suggest paths that exist
		sugg = slices.DeleteFunc(sugg, func(p string) bool { return !slices.Contains(m.indexPaths, p) })
	}
	m.modal = newSearch(m, "Diff "+cur+" against…", sugg,
		func(m *Model, other string) tea.Cmd {
			return m.loadDiff(cur+"  ↔  "+other, cur, 0, other, 0)
		})
	return nil
}

func (m *Model) loadDiff(title, leftPath string, leftVer int, rightPath string, rightVer int) tea.Cmd {
	read := func(ctx context.Context, full string, ver int) (*vault.Secret, error) {
		mount, path, ok := vault.SplitPath(full, m.mounts)
		if !ok {
			return nil, fmt.Errorf("no KV mount for %s", full)
		}
		s, err := m.Store.Read(ctx, mount, path, ver)
		if errors.Is(err, vault.ErrForbidden) {
			return nil, fmt.Errorf("🔒 no access to %s", full)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", full, err)
		}
		return s, nil
	}
	return m.do(func(ctx context.Context) tea.Msg {
		l, err := read(ctx, leftPath, leftVer)
		if err != nil {
			return diffMsg{err: err}
		}
		r, err := read(ctx, rightPath, rightVer)
		return diffMsg{title, l, r, err}
	})
}

func newDiffModal(msg diffMsg) *diffModal {
	d := &diffModal{title: msg.title, rows: diff.Keys(msg.left.Data, msg.right.Data), revealed: map[string]bool{}}
	d.filter()
	return d
}

func (d *diffModal) filter() {
	d.shown = d.shown[:0]
	for _, r := range d.rows {
		if d.showSame || r.Status != diff.Same {
			d.shown = append(d.shown, r)
		}
	}
	d.cursor = clamp(d.cursor, 0, max(len(d.shown)-1, 0))
}

func (d *diffModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	if c, ok := m.nav(msg, d.cursor, len(d.shown)); ok {
		d.cursor = c
		return nil
	}
	switch msg.String() {
	case "esc", "q":
		m.modal = nil
	case "s":
		d.showSame = !d.showSame
		d.filter()
	case "r":
		if d.cursor < len(d.shown) {
			k := d.shown[d.cursor].Key
			d.revealed[k] = !d.revealed[k]
		}
	}
	return nil
}

func (d *diffModal) view(m *Model, w, h int) string {
	w = min(w, 120)
	lines := []string{
		stTitle.Render(ansi.Truncate(d.title, w, "…")),
		diff.Summary(d.rows) + stDim.Render("  · r reveal · s show/hide same · esc close"),
	}
	kw := 3
	for _, r := range d.shown {
		kw = max(kw, min(len(r.Key), 30))
	}
	vw := max((w-kw-6)/2, 8)
	lines = append(lines, stDim.Render(fmt.Sprintf("  %-*s %-*s %s", kw, "KEY", vw, "LEFT", "RIGHT")))
	if len(d.shown) == 0 {
		lines = append(lines, stDim.Render("  no differences (s shows identical keys)"))
	}
	cell := func(v string, present, show bool) string {
		switch {
		case !present:
			return "–"
		case show:
			return ansi.Truncate(oneLine(v), vw, "…")
		}
		return mask
	}
	var extra []string
	if d.cursor < len(d.shown) {
		sel := d.shown[d.cursor]
		if sel.Status == diff.Changed && d.revealed[sel.Key] && diff.NeedsCharDiff(sel.Left, sel.Right) {
			var b strings.Builder
			for _, c := range diff.Chars(sel.Left, sel.Right) {
				switch c.Type {
				case diffmatchpatch.DiffInsert:
					b.WriteString(stAdded.Underline(true).Render(c.Text))
				case diffmatchpatch.DiffDelete:
					b.WriteString(stRemoved.Strikethrough(true).Render(c.Text))
				default:
					b.WriteString(c.Text)
				}
			}
			extra = append([]string{"", stDim.Render("char diff · " + sel.Key)}, strings.Split(ansi.Wrap(b.String(), w, ""), "\n")...)
			extra = extra[:min(len(extra), max(h/2, 3))]
		}
	}
	rows := max(h-len(lines)-len(extra)-2, 1)
	d.offset = scroll(d.cursor, d.offset, rows)
	style := map[diff.Status]lipgloss.Style{diff.Added: stAdded, diff.Removed: stRemoved, diff.Changed: stChanged, diff.Same: stDim}
	for i := d.offset; i < len(d.shown) && i < d.offset+rows; i++ {
		r := d.shown[i]
		show := d.revealed[r.Key]
		line := fmt.Sprintf("%c %-*s %s %s", r.Status, kw, ansi.Truncate(r.Key, kw, "…"),
			pad(cell(r.Left, r.Status != diff.Added, show), vw), cell(r.Right, r.Status != diff.Removed, show))
		if i == d.cursor {
			line = stSel.Render(pad(line, w))
		} else {
			line = style[r.Status].Render(line)
		}
		lines = append(lines, line)
	}
	lines = append(lines, extra...)
	return titled(lines, w)
}

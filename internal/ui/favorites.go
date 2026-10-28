package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type favoritesModal struct {
	cursor  int
	editing bool
	alias   textinput.Model
}

func (m *Model) toggleFavorite() {
	p := m.sec.path
	if n := m.tree.selected(); m.focus == 0 && n != nil && !n.isDir {
		p = n.full()
	}
	if p == "" {
		return
	}
	if m.Config.ToggleFavorite(p) {
		m.flash("★ added "+p, false)
	} else {
		m.flash("removed favorite "+p, false)
	}
	m.saveConfig()
}

func (f *favoritesModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	favs := m.Config.Favorites
	if f.editing {
		switch msg.String() {
		case "enter":
			favs[f.cursor].Alias = strings.TrimSpace(f.alias.Value())
			m.saveConfig()
			f.editing = false
		case "esc":
			f.editing = false
		default:
			var cmd tea.Cmd
			f.alias, cmd = f.alias.Update(msg)
			return cmd
		}
		return nil
	}
	if c, ok := m.nav(msg, f.cursor, len(favs)); ok {
		f.cursor = c
		return nil
	}
	switch msg.String() {
	case "esc", "q", "F":
		m.modal = nil
	case "a":
		if len(favs) > 0 {
			f.alias = textinput.New()
			f.alias.Prompt = "alias: "
			f.alias.SetValue(favs[f.cursor].Alias)
			f.alias.Focus()
			f.editing = true
			return textinput.Blink
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if i := int(msg.String()[0] - '1'); i < len(favs) {
			m.modal = nil
			m.focus = 1
			return m.jumpTo(favs[i].Path)
		}
	case "enter":
		if len(favs) > 0 {
			m.modal = nil
			m.focus = 1
			return m.jumpTo(favs[f.cursor].Path)
		}
	}
	return nil
}

func (f *favoritesModal) view(m *Model, w, h int) string {
	w = min(w, 80)
	lines := []string{stTitle.Render("★ Favorites"), stDim.Render("enter or 1-9 jump · a set alias · esc close"), ""}
	favs := m.Config.Favorites
	if len(favs) == 0 {
		lines = append(lines, stDim.Render("No favorites yet. Press f on a secret to add one."))
	}
	aw := 0
	for _, fv := range favs {
		aw = max(aw, len(fv.Alias))
	}
	for i, fv := range favs {
		if i >= h-4 {
			break
		}
		num := stKeyCap.Render(" ")
		if i < 9 {
			num = stKeyCap.Render(fmt.Sprint(i + 1))
		}
		line := " " + num + "  " + pad(fv.Alias, aw) + "  " + stDim.Render(fv.Path)
		if aw == 0 {
			line = " " + num + "  " + fv.Path
		}
		line = ansi.Truncate(line, w, "…")
		if i == f.cursor {
			line = stSel.Render(pad(line, w))
		}
		lines = append(lines, line)
	}
	if f.editing {
		lines = append(lines, "", f.alias.View())
	}
	return titled(lines, w)
}

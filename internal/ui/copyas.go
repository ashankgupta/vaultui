package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"vaultui/internal/export"
)

// copyAsModal copies the open secret in a chosen format (shell, k8s...).
// The preview is rendered from masked values, so nothing is revealed.
type copyAsModal struct{ cursor int }

func (c *copyAsModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	if i, ok := m.nav(msg, c.cursor, len(export.Formats)); ok {
		c.cursor = i
		return nil
	}
	switch k := msg.String(); k {
	case "esc", "q", "x":
		m.modal = nil
	case "1", "2", "3", "4", "5":
		if i := int(k[0] - '1'); i < len(export.Formats) {
			c.cursor = i
			return c.copy(m)
		}
	case "enter", "y", "c":
		return c.copy(m)
	}
	return nil
}

func (c *copyAsModal) copy(m *Model) tea.Cmd {
	s := m.sec.sec
	f := export.Formats[c.cursor]
	out, err := export.Render(f.ID, leafName(s.FullPath()), s.Data, export.Options{})
	m.modal = nil
	if err != nil {
		m.flash(err.Error(), true)
		return nil
	}
	m.copy(fmt.Sprintf("%s as %s", leafName(s.FullPath()), f.ID), out, true)
	return nil
}

func (c *copyAsModal) view(m *Model, w, h int) string {
	w = min(w, 90)
	lines := []string{stTitle.Render("Copy " + leafName(m.sec.path) + " as…"), ""}
	for i, f := range export.Formats {
		line := stKeyCap.Render(fmt.Sprint(i+1)) + "  " + stText.Render(f.Label)
		if i == c.cursor {
			line = stSel.Render(pad(ansi.Strip(line), w))
		}
		lines = append(lines, line)
	}

	masked := map[string]string{}
	for k := range m.sec.sec.Data {
		masked[k] = mask
	}
	prev, _ := export.Render(export.Formats[c.cursor].ID, leafName(m.sec.path), masked, export.Options{})
	pl := strings.Split(strings.TrimRight(prev, "\n"), "\n")
	room := max(h-len(lines)-6, 3)
	if len(pl) > room {
		pl = append(pl[:room-1], "…")
	}
	lines = append(lines, "", stSection.Render("Preview")+stDim.Render("  (values masked; the clipboard gets the real ones)"))
	for _, l := range pl {
		lines = append(lines, stDim.Render("  ")+stMuted.Render(ansi.Truncate(l, w-2, "…")))
	}
	lines = append(lines, "", stDim.Render(fmt.Sprintf("enter copy · 1-5 pick and copy · clipboard clears after %ds · esc close", m.Config.UI.ClipboardClearSeconds)))
	return titled(lines, w)
}

func leafName(p string) string {
	p = strings.TrimSuffix(p, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

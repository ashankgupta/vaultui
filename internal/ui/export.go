package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"vaultui/internal/export"
	"vaultui/internal/vault"
)

const (
	fPath = iota
	fMode
	fTransform
	fPrefix
	fCount
)

type exportModal struct {
	sec        *vault.Secret
	path       textinput.Model
	prefix     textinput.Model
	appendMode bool
	upper      bool
	field      int
	confirm    bool // waiting for "y" because the file isn't git-ignored
}

func newExport(s *vault.Secret) *exportModal {
	p := textinput.New()
	p.SetValue("./.env")
	p.Focus()
	pre := textinput.New()
	pre.Placeholder = "e.g. PAYMENTS_"
	return &exportModal{sec: s, path: p, prefix: pre}
}

func (e *exportModal) focusField(f int) {
	e.field = (f + fCount) % fCount
	e.path.Blur()
	e.prefix.Blur()
	switch e.field {
	case fPath:
		e.path.Focus()
	case fPrefix:
		e.prefix.Focus()
	}
}

func (e *exportModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	if e.confirm {
		e.confirm = false
		if msg.String() == "y" {
			return e.write(m)
		}
		m.flash("export cancelled", false)
		return nil
	}
	switch msg.String() {
	case "esc":
		m.modal = nil
		return nil
	case "tab", "down":
		e.focusField(e.field + 1)
		return nil
	case "shift+tab", "up":
		e.focusField(e.field - 1)
		return nil
	case "enter":
		if in, ignored := export.GitIgnored(e.path.Value()); in && !ignored {
			e.confirm = true
			return nil
		}
		return e.write(m)
	case " ", "left", "right":
		switch e.field {
		case fMode:
			e.appendMode = !e.appendMode
			return nil
		case fTransform:
			e.upper = !e.upper
			return nil
		}
	}
	var cmd tea.Cmd
	switch e.field {
	case fPath:
		e.path, cmd = e.path.Update(msg)
	case fPrefix:
		e.prefix, cmd = e.prefix.Update(msg)
	}
	return cmd
}

func (e *exportModal) write(m *Model) tea.Cmd {
	out := export.Format(e.sec.Data, export.Options{Upper: e.upper, Prefix: e.prefix.Value()})
	if err := export.Write(e.path.Value(), out, e.appendMode); err != nil {
		m.flash("export failed: "+err.Error(), true)
		return nil
	}
	m.modal = nil
	m.flash(fmt.Sprintf("Wrote %d keys to %s (0600)", len(e.sec.Data), e.path.Value()), false)
	return nil
}

func (e *exportModal) view(m *Model, w, h int) string {
	w = min(w, 70)
	e.path.Width, e.prefix.Width = w-14, w-14
	opt := func(on bool, a, b string) string {
		if on {
			return stDim.Render(a+" / ") + stTitle.Render("["+b+"]")
		}
		return stTitle.Render("["+a+"]") + stDim.Render(" / "+b)
	}
	label := func(f int, s string) string {
		if e.field == f {
			return stHeader.Render("▸ " + s)
		}
		return "  " + s
	}
	lines := []string{
		stTitle.Render("Export " + e.sec.FullPath() + " to .env"),
		"",
		label(fPath, "Output    ") + e.path.View(),
		label(fMode, "Mode      ") + opt(e.appendMode, "overwrite", "append"),
		label(fTransform, "Keys      ") + opt(e.upper, "as-is", "UPPER_SNAKE_CASE"),
		label(fPrefix, "Prefix    ") + e.prefix.View(),
		"",
		stWarnBold.Render("This writes secrets to disk in plain text. Make sure the file is in .gitignore."),
	}
	if e.confirm {
		lines = append(lines, "", stErr.Render(e.path.Value()+" is inside a git repo and is NOT git-ignored!"),
			stErr.Render("Press y to write anyway, any other key to cancel."))
	} else {
		lines = append(lines, stDim.Render("tab next field · space toggle · enter write · esc cancel"))
	}
	return titled(lines, w)
}

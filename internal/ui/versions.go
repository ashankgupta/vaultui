package ui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"vaultui/internal/vault"
)

type versionsMsg struct {
	path string
	vs   []vault.VersionMeta
	err  error
}

type versionsModal struct {
	path   string
	vs     []vault.VersionMeta
	cursor int
}

func (m *Model) openVersions() tea.Cmd {
	s := m.sec.sec
	if s == nil {
		return nil
	}
	if s.KVVersion == 1 {
		m.flash("Version history is not available for KV v1", true)
		return nil
	}
	return m.do(func(ctx context.Context) tea.Msg {
		vs, err := m.Store.Versions(ctx, s.Mount, s.Path)
		return versionsMsg{s.FullPath(), vs, err}
	})
}

func (v *versionsModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	if c, ok := m.nav(msg, v.cursor, len(v.vs)); ok {
		v.cursor = c
		return nil
	}
	switch msg.String() {
	case "esc", "q", "v":
		m.modal = nil
	case "enter":
		n := v.vs[v.cursor].Version
		m.modal = nil
		return m.openSecretVersion(v.path, n)
	case "d":
		n := v.vs[v.cursor].Version
		m.modal = nil
		return m.loadDiff(fmt.Sprintf("%s  v%d ↔ latest", v.path, n), v.path, n, v.path, 0)
	}
	return nil
}

func (v *versionsModal) view(m *Model, w, h int) string {
	w = min(w, 70)
	lines := []string{stTitle.Render("Versions · " + v.path), stDim.Render("enter view · d diff vs latest · esc close")}
	for i, vm := range v.vs {
		if i >= h-4 {
			break
		}
		state := ""
		switch {
		case vm.Destroyed:
			state = stErr.Render("destroyed")
		case vm.Deleted:
			state = stErr.Render("deleted")
		}
		line := fmt.Sprintf(" v%-4d %s  %-10s %s", vm.Version, vm.Created.Local().Format("2006-01-02 15:04"), stDim.Render(ago(vm.Created)), state)
		if i == 0 {
			line += stDim.Render(" (latest)")
		}
		if i == v.cursor {
			line = stSel.Render(pad(line, w))
		}
		lines = append(lines, line)
	}
	return titled(lines, w)
}

// openSecretVersion shows a specific version in the secret pane.
func (m *Model) openSecretVersion(full string, version int) tea.Cmd {
	m.sec = newSecretPane(full)
	mount, path, _ := vault.SplitPath(full, m.mounts)
	return m.do(func(ctx context.Context) tea.Msg {
		s, err := m.Store.Read(ctx, mount, path, version)
		return secretMsg{full, nil, s, err}
	})
}

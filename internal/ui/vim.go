package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// vimState holds a pending count ("12") and prefix key ("g", "z", "'",
// "ctrl+w", "Z") between keystrokes.
type vimState struct {
	count   int
	pending string
}

// showcmd is what vim prints in the corner while you type "5g".
func (v vimState) showcmd() string {
	s := v.pending
	if v.count > 0 {
		s = strconv.Itoa(v.count) + s
	}
	return s
}

func (m *Model) takeCount() int {
	c := max(m.vim.count, 1)
	m.vim.count = 0
	return c
}

func (m *Model) halfPage() int { return max((m.height-8)/2, 1) }

// nav applies vim/arrow list movement to cursor over n items. It is shared by
// the panes and the list modals.
func (m *Model) nav(msg tea.KeyMsg, cursor, n int) (int, bool) {
	if n == 0 {
		return 0, false
	}
	k := msg.String()
	if m.vim.pending == "g" {
		m.vim.pending = ""
		if k == "esc" {
			m.vim.count = 0
			return cursor, false
		}
		if k == "g" { // gg, or {count}gg
			return clamp(m.takeCount()-1, 0, n-1), true
		}
		m.vim.count = 0
		return cursor, true
	}
	step := 0
	switch k {
	case "j", "down":
		step = 1
	case "k", "up":
		step = -1
	case "ctrl+d":
		step = m.halfPage()
	case "ctrl+u":
		step = -m.halfPage()
	case "ctrl+f", "pgdown":
		step = 2 * m.halfPage()
	case "ctrl+b", "pgup":
		step = -2 * m.halfPage()
	case "G", "end":
		if m.vim.count > 0 {
			return clamp(m.takeCount()-1, 0, n-1), true
		}
		return n - 1, true
	case "home":
		return 0, true
	case "g":
		m.vim.pending = "g"
		return cursor, true
	default:
		return cursor, false
	}
	return clamp(cursor+step*m.takeCount(), 0, n-1), true
}

// vimKey handles counts, prefixes and vim-only motions in the main view.
// It returns handled=false for keys the regular keymap should process.
func (m *Model) vimKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	k := msg.String()
	if k == "esc" {
		m.vim = vimState{}
		if m.sec.filter != "" && m.focus == 1 {
			m.sec.filter = ""
			m.sec.applyFilter()
		}
		if m.focus == 0 {
			m.tree.query = "" // like :noh
		}
		return nil, true
	}

	switch m.vim.pending {
	case "'":
		m.vim = vimState{}
		if i, err := strconv.Atoi(k); err == nil && i >= 1 && i <= len(m.Config.Favorites) {
			m.focus = 1
			return m.jumpTo(m.Config.Favorites[i-1].Path), true
		}
		return nil, true
	case "z":
		m.vim = vimState{}
		return m.fold(k), true
	case "Z":
		m.vim = vimState{}
		if k == "Z" || k == "Q" {
			return m.quit(), true
		}
		return nil, true
	case "ctrl+w":
		m.vim = vimState{}
		switch k {
		case "h", "left":
			m.focus = 0
		case "l", "right":
			m.focus = 1
		case "w", "ctrl+w", "p":
			m.focus = 1 - m.focus
		}
		return nil, true
	}

	if len(k) == 1 && k[0] >= '0' && k[0] <= '9' && (k != "0" || m.vim.count > 0) {
		m.vim.count = min(m.vim.count*10+int(k[0]-'0'), 99999)
		return nil, true
	}

	switch k {
	case "'", "z", "Z", "ctrl+w":
		m.vim.pending = k
		return nil, true
	case ":":
		m.vim = vimState{}
		in := textinput.New()
		in.Prompt = ":"
		in.Placeholder = "e <path> · diff <path> · reindex · help · q"
		in.Focus()
		m.cmdline = &in
		return textinput.Blink, true
	case "n", "N":
		if p := &m.sec; m.focus == 1 && p.filter != "" && len(p.keys) > 0 { // next key match
			step := m.takeCount()
			if k == "N" {
				step = -step
			}
			p.cursor = ((p.cursor+step)%len(p.keys) + len(p.keys)) % len(p.keys)
			return nil, true
		}
		if t := &m.tree; m.focus == 0 && t.query != "" {
			dir := 1
			if k == "N" {
				dir = -1
			}
			for i := m.takeCount(); i > 0; i-- {
				t.findFrom(t.cursor+dir, dir)
			}
			return nil, true
		}
		return m.nextHit(k == "n"), true
	case "-":
		if n := m.tree.selected(); m.focus == 0 && n != nil && n.parent != nil {
			n = n.parent
			n.expanded = false
			m.tree.rebuild()
			m.tree.selectNode(n)
		}
		m.vim.count = 0
		return nil, true
	}

	if m.focus == 1 && m.sec.sec != nil {
		if c, ok := m.nav(msg, m.sec.cursor, len(m.sec.keys)); ok {
			m.sec.cursor = c
			return nil, true
		}
	} else if m.focus == 0 {
		if c, ok := m.nav(msg, m.tree.cursor, len(m.tree.visible)); ok {
			m.tree.cursor = c
			return nil, true
		}
	}
	m.vim.count = 0
	return nil, false
}

// fold implements zo/zc/za/zM on the tree.
func (m *Model) fold(k string) tea.Cmd {
	if k == "M" {
		var walk func([]*node)
		walk = func(ns []*node) {
			for _, n := range ns {
				n.expanded = false
				walk(n.children)
			}
		}
		walk(m.tree.roots)
		m.tree.cursor = 0
		m.tree.rebuild()
		return nil
	}
	n := m.tree.selected()
	if n == nil {
		return nil
	}
	if !n.isDir && n.parent != nil {
		n = n.parent
	}
	switch k {
	case "o":
		n.expanded = true
	case "c":
		n.expanded = false
	case "a":
		n.expanded = !n.expanded
	default:
		return nil
	}
	m.tree.rebuild()
	m.tree.selectNode(n)
	if n.expanded && !n.loaded && !n.locked {
		return m.loadChildren(n)
	}
	return nil
}

// nextHit walks the results of the last search, like vim's n / N.
func (m *Model) nextHit(forward bool) tea.Cmd {
	if len(m.hits) == 0 {
		m.flash("no previous search (use /)", true)
		return nil
	}
	step := m.takeCount()
	if !forward {
		step = -step
	}
	m.hitIdx = ((m.hitIdx+step)%len(m.hits) + len(m.hits)) % len(m.hits)
	m.flash(strings.Join([]string{"match", strconv.Itoa(m.hitIdx + 1), "of", strconv.Itoa(len(m.hits)), "·", m.hitQuery}, " "), false)
	m.focus = 1
	return m.jumpTo(m.hits[m.hitIdx])
}

// --- : command line ---

func (m *Model) cmdlineKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.cmdline = nil
		return nil
	case "enter":
		line := strings.TrimSpace(m.cmdline.Value())
		m.cmdline = nil
		return m.runCommand(line)
	case "tab":
		m.completeCmdline()
		return nil
	}
	var cmd tea.Cmd
	*m.cmdline, cmd = m.cmdline.Update(msg)
	return cmd
}

func (m *Model) runCommand(line string) tea.Cmd {
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "":
		return nil
	case "q", "q!", "qa", "qa!", "quit", "wq", "x":
		return m.quit()
	case "e", "edit", "o", "open", "cd", "goto":
		if arg == "" {
			m.flash(":"+name+" needs a path", true)
			return nil
		}
		m.focus = 1
		return m.jumpTo(arg)
	case "diff":
		if m.sec.sec == nil || arg == "" {
			m.flash(":diff <path> compares the open secret with another path", true)
			return nil
		}
		return m.loadDiff(m.sec.path+"  ↔  "+arg, m.sec.path, 0, arg, 0)
	case "vault", "vaults", "switch":
		m.SwitchVault = true
		return m.quit()
	case "reindex":
		return m.startCrawl()
	case "h", "help":
		m.modal = helpModal{}
		return nil
	case "fav", "favorites":
		m.modal = &favoritesModal{}
		return nil
	}
	if strings.Contains(name, "/") { // ":secret/foo/bar" jumps straight there
		m.focus = 1
		return m.jumpTo(line)
	}
	if n, err := strconv.Atoi(name); err == nil { // ":42" goes to line 42
		if m.focus == 1 {
			m.sec.cursor = clamp(n-1, 0, max(len(m.sec.keys)-1, 0))
		} else {
			m.tree.cursor = clamp(n-1, 0, max(len(m.tree.visible)-1, 0))
		}
		return nil
	}
	m.flash("E492: Not an editor command: "+line, true)
	return nil
}

// completeCmdline extends the path argument to the longest common prefix of
// indexed paths, stopping at the next "/".
func (m *Model) completeCmdline() {
	v := m.cmdline.Value()
	cmd, arg, hasArg := strings.Cut(v, " ")
	if !hasArg {
		cmd, arg = "", v
	}
	var common string
	found := false
	for _, p := range m.indexPaths {
		if !strings.HasPrefix(p, arg) {
			continue
		}
		next := p
		if i := strings.Index(p[len(arg):], "/"); i >= 0 {
			next = p[:len(arg)+i+1]
		}
		if !found {
			common, found = next, true
			continue
		}
		for !strings.HasPrefix(next, common) {
			common = common[:len(common)-1]
		}
	}
	if !found || len(common) <= len(arg) {
		return
	}
	if cmd != "" {
		common = cmd + " " + common
	}
	m.cmdline.SetValue(common)
	m.cmdline.CursorEnd()
}

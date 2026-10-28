package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

type keyMap struct {
	Up, Down, Right, Left, Tab                      key.Binding
	Search, GlobalSearch, Copy, CopyJSON            key.Binding
	CopyPath, CopyAs                                key.Binding
	Reveal, RevealAll, Versions, Diff               key.Binding
	Export, Fav, Favs, Refresh, Help, Quit          key.Binding
	Relogin, Inspect, OpenWeb, FavJump, SwitchVault key.Binding
}

func kb(keys []string, help, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...), key.WithHelp(help, desc))
}

var keys = keyMap{
	Up:           kb([]string{"up", "k"}, "↑/k", "up"),
	Down:         kb([]string{"down", "j"}, "↓/j", "down"),
	Right:        kb([]string{"right", "l", "enter"}, "enter", "open"),
	Left:         kb([]string{"left", "h"}, "←/h", "collapse"),
	Tab:          kb([]string{"tab"}, "tab", "switch pane"),
	Search:       kb([]string{"/"}, "/", "find in pane"),
	GlobalSearch: kb([]string{"ctrl+p"}, "ctrl+p", "search all secrets"),
	Copy:         kb([]string{"c"}, "c", "copy"),
	CopyJSON:     kb([]string{"C"}, "C", "copy JSON"),
	CopyPath:     kb([]string{"y"}, "y", "copy path"),
	CopyAs:       kb([]string{"x"}, "x", "copy as shell/k8s/docker…"),
	Reveal:       kb([]string{"r"}, "r", "reveal"),
	RevealAll:    kb([]string{"R"}, "R", "reveal all"),
	Versions:     kb([]string{"v"}, "v", "versions"),
	Diff:         kb([]string{"d"}, "d", "diff"),
	Export:       kb([]string{"e"}, "e", "export"),
	Fav:          kb([]string{"f"}, "f", "★"),
	Favs:         kb([]string{"F"}, "F", "favorites"),
	Refresh:      kb([]string{"ctrl+r"}, "ctrl+r", "refresh"),
	Help:         kb([]string{"?"}, "?", "help"),
	Quit:         kb([]string{"q", "ctrl+c"}, "q", "quit"),
	// TODO(plan): not in the keybinding table; added for the M7 re-login prompt.
	Relogin:     kb([]string{"L"}, "L", "re-login"),
	Inspect:     kb([]string{"enter"}, "enter", "inspect value"),
	OpenWeb:     kb([]string{"o"}, "o", "open in web UI"),
	FavJump:     kb([]string{"'"}, "'1-'9", "jump to favorite"),
	SwitchVault: kb([]string{"V"}, "V", "switch Vault"),
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Search, k.Right, k.Copy, k.Reveal, k.Versions, k.Diff, k.Export, k.Fav, k.Help}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Right, k.Left, k.Tab},
		{kb([]string{"_"}, "gg/G", "top/bottom"), kb([]string{"_"}, "5j 10G", "counts"), kb([]string{"_"}, "ctrl+d/u", "half page"), kb([]string{"_"}, "ctrl+f/b", "page"), kb([]string{"_"}, "n/N", "next/prev match"), kb([]string{"_"}, "-", "parent folder")},
		{kb([]string{"_"}, "zo/zc/za", "fold"), kb([]string{"_"}, "zM", "collapse all"), kb([]string{"_"}, "ctrl+w h/l", "pane"), kb([]string{"_"}, ":", "command"), kb([]string{"_"}, "ZZ/:q", "quit")},
		{k.Search, k.GlobalSearch, k.Copy, k.CopyJSON, k.CopyAs, k.CopyPath, k.Reveal, k.RevealAll},
		{k.Inspect, k.Versions, k.Diff, k.Export, k.OpenWeb},
		{k.Fav, k.Favs, k.FavJump, k.Refresh, k.SwitchVault, k.Relogin, k.Help, k.Quit},
	}
}

type helpModal struct{}

func (helpModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	m.modal = nil // any key closes
	return nil
}

func (helpModal) view(m *Model, w, h int) string {
	hm := m.help
	hm.ShowAll = true
	extra := stDim.Render(":e <path> (tab completes) · :diff <path> · :reindex · :vault · :42 goes to line 42\nIn modals: esc closes · search: ↑/↓ move, ctrl+r reindex · diff: s toggle identical keys, r reveal row\nMouse: click to select/open, wheel to scroll · Values re-mask after " +
		fmt.Sprint(m.Config.UI.RevealTimeoutSeconds) + "s · clipboard clears after " + fmt.Sprint(m.Config.UI.ClipboardClearSeconds) + "s")
	return modalBox("Keys", []string{"", hm.View(keys), "", extra}, max(min(w, 110), 20))
}

// footerKeys are the context-sensitive hints for the bottom bar.
func (m *Model) footerKeys() []key.Binding {
	esc := kb([]string{"esc"}, "esc", "close")
	if m.tree.search != nil {
		return []key.Binding{kb([]string{"_"}, "type", "find"), kb([]string{"_"}, "ctrl+j/k", "next/prev"), kb([]string{"_"}, "enter", "stay here"), kb([]string{"_"}, "esc", "cancel")}
	}
	switch m.modal.(type) {
	case nil:
	case *searchModal:
		return []key.Binding{kb([]string{"_"}, "type", "filter"), kb([]string{"_"}, "↑/↓", "move"), kb([]string{"_"}, "enter", "select"), kb([]string{"_"}, "ctrl+r", "reindex"), esc}
	case *diffModal:
		return []key.Binding{kb([]string{"_"}, "↑/↓", "move"), kb([]string{"_"}, "r", "reveal row"), kb([]string{"_"}, "s", "show same"), esc}
	case *versionsModal:
		return []key.Binding{kb([]string{"_"}, "↑/↓", "move"), kb([]string{"_"}, "enter", "view"), kb([]string{"_"}, "d", "diff vs latest"), esc}
	case *inspectModal:
		return []key.Binding{kb([]string{"_"}, "r", "reveal"), kb([]string{"_"}, "c", "copy"), kb([]string{"_"}, "↑/↓", "scroll"), esc}
	case *favoritesModal:
		return []key.Binding{kb([]string{"_"}, "enter", "jump"), kb([]string{"_"}, "1-9", "jump"), kb([]string{"_"}, "a", "alias"), kb([]string{"_"}, "j/k gg G", "move"), esc}
	case *copyAsModal:
		return []key.Binding{kb([]string{"_"}, "j/k", "move"), kb([]string{"_"}, "enter", "copy"), kb([]string{"_"}, "1-5", "pick & copy"), esc}
	case *exportModal:
		return []key.Binding{kb([]string{"_"}, "tab", "next field"), kb([]string{"_"}, "space", "toggle"), kb([]string{"_"}, "enter", "write"), esc}
	default:
		return []key.Binding{esc}
	}
	if m.focus == 1 && m.sec.sec != nil {
		if m.sec.search != nil {
			return []key.Binding{kb([]string{"_"}, "type", "filter keys"), kb([]string{"_"}, "ctrl+j/k", "move"), kb([]string{"_"}, "enter", "keep filter"), kb([]string{"_"}, "esc", "clear")}
		}
		return []key.Binding{kb([]string{"_"}, "j/k", "select"), kb([]string{"_"}, "/", "filter keys"), kb([]string{"_"}, "ctrl+p", "search all"), keys.Inspect, keys.Reveal, keys.Copy, keys.CopyAs, keys.Versions, keys.Diff, keys.Export, keys.OpenWeb, kb([]string{"_"}, "tab", "tree"), keys.Help}
	}
	return []key.Binding{kb([]string{"_"}, "j/k", "move"), kb([]string{"_"}, "l/enter", "open"), kb([]string{"_"}, "h", "collapse"), kb([]string{"_"}, "/", "find in tree"), kb([]string{"_"}, "ctrl+p", "search all"), kb([]string{"_"}, ":", "cmd"), keys.Fav, keys.Favs, kb([]string{"_"}, "tab", "secret"), keys.Help, keys.Quit}
}

// Package ui is the Bubble Tea front end. All Vault calls run as tea.Cmds.
package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/pkg/browser"

	"vaultui/internal/auth"
	"vaultui/internal/clipboard"
	"vaultui/internal/config"
	"vaultui/internal/vault"
)

type Options struct {
	Store  vault.KVStore
	Config *config.Config
	Info   *auth.Info
	Addr   string
	// CacheDir holds the path index; empty disables the disk cache.
	CacheDir string
	// VaultName is the saved Vault in use ("" when it came from VAULT_ADDR).
	VaultName string
	// Relogin runs the OIDC flow again; nil disables re-login.
	Relogin func(ctx context.Context) (*auth.Info, error)
}

// reloginWarn is when the header starts nagging about token expiry.
const reloginWarn = 10 * time.Minute

// modal is a view that takes over the keyboard (search, versions, diff...).
type modal interface {
	update(m *Model, msg tea.KeyMsg) tea.Cmd
	view(m *Model, w, h int) string
}

type Model struct {
	Options
	mounts []vault.Mount
	tree   tree
	sec    secretPane
	focus  int // 0 tree, 1 secret
	modal  modal

	spin     spinner.Model
	inflight int
	help     help.Model
	width    int
	height   int
	fatal    error

	toast      string
	toastErr   bool
	toastUntil time.Time

	pendingJump string

	indexPaths   []string
	indexUpdated time.Time
	indexing     bool
	indexSet     map[string]bool
	crawlPaths   []string
	crawlGen     int
	crawlCancel  context.CancelFunc
	indexCh      chan tea.Msg

	clip      *clipboard.Manager
	clipLabel string
	clipUntil time.Time

	// SwitchVault is set when the user quits to pick another Vault.
	SwitchVault bool

	vim      vimState
	cmdline  *textinput.Model
	hits     []string // results of the last search, for n / N
	hitIdx   int
	hitQuery string
}

func New(o Options) *Model {
	applyTheme(o.Config.UI.Theme)
	return &Model{
		Options: o,
		spin:    spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		help:    help.New(),
		clip:    clipboard.New(),
	}
}

// --- messages ---

type doneMsg struct{ msg tea.Msg }
type mountsMsg struct {
	mounts []vault.Mount
	err    error
}
type listMsg struct {
	n   *node
	es  []vault.Entry
	err error
}
type secretMsg struct {
	path string
	n    *node
	sec  *vault.Secret
	err  error
}
type secondMsg struct{}
type remaskMsg struct{ seq int }
type reloginMsg struct {
	info *auth.Info
	err  error
}

func everySecond() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return secondMsg{} })
}

// do runs f in the background with a timeout and tracks it for the spinner.
func (m *Model) do(f func(ctx context.Context) tea.Msg) tea.Cmd {
	m.inflight++
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return doneMsg{f(ctx)}
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, everySecond(), m.do(func(ctx context.Context) tea.Msg {
		ms, err := m.Store.Mounts(ctx)
		return mountsMsg{ms, err}
	}))
}

func (m *Model) loadChildren(n *node) tea.Cmd {
	n.loading = true
	return m.do(func(ctx context.Context) tea.Msg {
		es, err := m.Store.List(ctx, n.mount, n.path)
		return listMsg{n, es, err}
	})
}

func (m *Model) openSecret(n *node, full string) tea.Cmd {
	m.sec = newSecretPane(full)
	m.Config.AddRecent(full)
	m.saveConfig()
	mount, path, ok := vault.SplitPath(full, m.mounts)
	if !ok {
		m.sec.err = fmt.Errorf("no KV mount for %s", full)
		return nil
	}
	return m.do(func(ctx context.Context) tea.Msg {
		s, err := m.Store.Read(ctx, mount, path, 0)
		return secretMsg{full, n, s, err}
	})
}

func (m *Model) saveConfig() {
	if err := m.Config.Save(); err != nil {
		m.flash("saving config: "+err.Error(), true)
	}
}

func (m *Model) flash(s string, isErr bool) {
	m.toast, m.toastErr, m.toastUntil = s, isErr, time.Now().Add(4*time.Second)
}

// jumpTo expands the tree down to full, loading folders as needed, then opens it.
func (m *Model) jumpTo(full string) tea.Cmd {
	m.pendingJump = full
	return m.advanceJump()
}

func (m *Model) advanceJump() tea.Cmd {
	if m.pendingJump == "" {
		return nil
	}
	n, exact := m.tree.deepest(m.pendingJump)
	if n == nil {
		m.flash("path not found: "+m.pendingJump, true)
		m.pendingJump = ""
		return nil
	}
	if exact {
		full := m.pendingJump
		m.pendingJump = ""
		m.tree.rebuild()
		m.tree.selectNode(n)
		if n.isDir {
			return nil
		}
		return m.openSecret(n, full)
	}
	if n.locked || n.loaded {
		// The tree can't reach it (folder not listable, or stale index):
		// select the closest folder and try reading the path directly.
		full := m.pendingJump
		m.pendingJump = ""
		n.expanded = !n.locked
		m.tree.rebuild()
		m.tree.selectNode(n)
		return m.openSecret(nil, full)
	}
	n.expanded = true
	return m.loadChildren(n)
}

// --- update ---

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case doneMsg:
		m.inflight--
		return m.Update(msg.msg)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.Width = msg.Width

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case secondMsg:
		if !m.toastUntil.IsZero() && time.Now().After(m.toastUntil) {
			m.toast = ""
		}
		m.tickClipboard()
		return m, everySecond()

	case remaskMsg:
		if msg.seq == m.sec.revealSeq {
			m.sec.revealed = map[string]bool{}
		}

	case mountsMsg:
		if msg.err != nil {
			m.fatal = msg.err
			return m, nil
		}
		m.mounts = msg.mounts
		m.tree = newTree(msg.mounts)
		m.loadIndexCache()
		if m.indexUpdated.IsZero() || time.Since(m.indexUpdated) > m.Config.Search.IndexTTL {
			return m, m.startCrawl()
		}

	case versionsMsg:
		switch {
		case errors.Is(msg.err, vault.ErrForbidden):
			m.flash("🔒 no access to version metadata for "+msg.path, true)
		case msg.err != nil:
			m.flash(msg.err.Error(), true)
		case len(msg.vs) == 0:
			m.flash("no versions", true)
		default:
			m.modal = &versionsModal{path: msg.path, vs: msg.vs}
		}

	case reloginMsg:
		m.inflight--
		if msg.err != nil {
			m.flash("re-login failed: "+msg.err.Error(), true)
		} else {
			m.Info = msg.info
			m.flash("Logged in again", false)
		}

	case diffMsg:
		if msg.err != nil {
			m.flash(msg.err.Error(), true)
		} else {
			m.modal = newDiffModal(msg)
		}

	case indexBatchMsg, indexDoneMsg:
		return m, m.onIndexMsg(msg)

	case listMsg:
		n := msg.n
		n.loading = false
		switch {
		case errors.Is(msg.err, vault.ErrForbidden):
			n.locked, n.loaded, n.expanded = true, true, false
			m.pendingJump = ""
		case msg.err != nil:
			n.expanded = false
			m.pendingJump = ""
			m.flash(msg.err.Error(), true)
		default:
			n.setChildren(msg.es)
		}
		m.tree.rebuild()
		return m, m.advanceJump()

	case secretMsg:
		if msg.path != m.sec.path {
			return m, nil // user moved on
		}
		switch {
		case errors.Is(msg.err, vault.ErrForbidden):
			m.sec.locked = true
			if msg.n != nil {
				msg.n.locked = true
			}
		case msg.err != nil:
			m.sec.err = msg.err
		default:
			m.sec.set(msg.sec)
		}

	case tea.KeyMsg:
		return m, m.handleKey(msg)

	case tea.MouseMsg:
		return m, m.handleMouse(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.cmdline != nil {
		return m.cmdlineKey(msg)
	}
	if m.sec.search != nil && m.modal == nil {
		return m.sec.searchKey(msg)
	}
	if m.tree.search != nil && m.modal == nil {
		return m.tree.searchKey(msg)
	}
	if msg.String() == "ctrl+c" {
		return m.quit()
	}
	if m.modal != nil {
		return m.modal.update(m, msg)
	}
	if cmd, ok := m.vimKey(msg); ok {
		return cmd
	}
	switch {
	case key.Matches(msg, keys.Quit):
		return m.quit()
	case key.Matches(msg, keys.Tab):
		m.focus = 1 - m.focus
	case key.Matches(msg, keys.Reveal):
		return m.reveal(false)
	case key.Matches(msg, keys.RevealAll):
		return m.reveal(true)
	case key.Matches(msg, keys.Refresh):
		return m.refresh()
	case key.Matches(msg, keys.SwitchVault):
		m.SwitchVault = true
		return m.quit()
	case key.Matches(msg, keys.Help):
		m.modal = helpModal{}
	case key.Matches(msg, keys.Relogin):
		if m.Relogin == nil {
			return nil
		}
		m.inflight++
		m.flash("Opening the browser for OIDC login…", false)
		return func() tea.Msg {
			info, err := m.Relogin(context.Background())
			return reloginMsg{info, err}
		}
	case key.Matches(msg, keys.Export):
		if m.sec.sec != nil {
			m.modal = newExport(m.sec.sec)
			return textinput.Blink
		}
	case key.Matches(msg, keys.Fav):
		m.toggleFavorite()
	case key.Matches(msg, keys.OpenWeb):
		return m.openWeb()
	case key.Matches(msg, keys.Favs):
		m.modal = &favoritesModal{}
	case key.Matches(msg, keys.Versions):
		return m.openVersions()
	case key.Matches(msg, keys.Diff):
		return m.openDiffPicker()
	case key.Matches(msg, keys.Search) && m.focus == 1 && m.sec.sec != nil:
		return m.sec.startSearch()
	case key.Matches(msg, keys.Search) && m.focus == 0 && len(m.tree.visible) > 0:
		return m.tree.startSearch()
	case key.Matches(msg, keys.Search), key.Matches(msg, keys.GlobalSearch):
		m.modal = newSearch(m, "Search all secrets", m.Config.Recent, nil)
		return textinput.Blink
	case key.Matches(msg, keys.Copy):
		if k := m.sec.selectedKey(); m.sec.sec != nil && k != "" {
			m.copy(k, m.sec.sec.Data[k], true)
		}
	case key.Matches(msg, keys.CopyAs):
		if m.sec.sec != nil && len(m.sec.all) > 0 {
			m.modal = &copyAsModal{}
		}
	case key.Matches(msg, keys.CopyJSON):
		if m.sec.sec != nil {
			b, _ := json.MarshalIndent(m.sec.sec.Data, "", "  ")
			m.copy("secret as JSON", string(b), true)
		}
	case key.Matches(msg, keys.CopyPath):
		p := m.sec.path
		if n := m.tree.selected(); m.focus == 0 && n != nil {
			p = n.full()
		}
		if p != "" {
			m.copy("path "+p, p, false)
		}
	}
	if m.focus == 1 {
		return m.secretKey(msg)
	}
	return m.treeKey(msg)
}

func (m *Model) quit() tea.Cmd {
	m.clip.ClearIfOwned()
	if m.crawlCancel != nil {
		m.crawlCancel()
	}
	return tea.Quit
}

// copy puts v on the clipboard; secrets get the auto-clear countdown.
func (m *Model) copy(label, v string, secret bool) {
	osc52, err := m.clip.Copy(v)
	switch {
	case err != nil:
		m.flash("copy failed: "+err.Error(), true)
		return
	case !secret:
		m.clipUntil = time.Time{}
		m.flash("Copied "+label, false)
	default:
		m.clipLabel = label
		m.clipUntil = time.Now().Add(time.Duration(m.Config.UI.ClipboardClearSeconds) * time.Second)
		m.tickClipboard()
	}
	if osc52 {
		m.flash("No system clipboard (install xclip, xsel or wl-copy); copied via OSC 52 terminal escape", true)
	}
}

func (m *Model) tickClipboard() {
	if m.clipUntil.IsZero() {
		return
	}
	left := time.Until(m.clipUntil).Round(time.Second)
	if left <= 0 {
		m.clipUntil = time.Time{}
		if m.clip.ClearIfOwned() {
			m.flash("Clipboard cleared", false)
		}
		return
	}
	total := max(m.Config.UI.ClipboardClearSeconds, 1)
	filled := min(int(left.Seconds())*10/total, 10)
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", 10-filled)
	m.toast, m.toastErr, m.toastUntil = fmt.Sprintf("Copied %s · clipboard clears in %ds %s", m.clipLabel, int(left.Seconds()), bar), false, m.clipUntil
}

func (m *Model) treeKey(msg tea.KeyMsg) tea.Cmd {
	t := &m.tree
	n := t.selected()
	switch {
	case key.Matches(msg, keys.Up):
		t.cursor = max(t.cursor-1, 0)
	case key.Matches(msg, keys.Down):
		t.cursor = min(t.cursor+1, len(t.visible)-1)
	case key.Matches(msg, keys.Right):
		if n == nil || n.locked {
			return nil
		}
		if !n.isDir {
			m.focus = 1
			return m.openSecret(n, n.full())
		}
		if msg.String() == "enter" && n.expanded {
			n.expanded = false
		} else {
			n.expanded = true
		}
		t.rebuild()
		if n.expanded && !n.loaded {
			return m.loadChildren(n)
		}
	case key.Matches(msg, keys.Left):
		if n == nil {
			return nil
		}
		if n.isDir && n.expanded {
			n.expanded = false
			t.rebuild()
		} else if n.parent != nil {
			t.selectNode(n.parent)
		}
	}
	return nil
}

func (m *Model) secretKey(msg tea.KeyMsg) tea.Cmd {
	p := &m.sec
	switch {
	case key.Matches(msg, keys.Up):
		p.cursor = max(p.cursor-1, 0)
	case key.Matches(msg, keys.Down):
		p.cursor = min(p.cursor+1, max(len(p.keys)-1, 0))
	case key.Matches(msg, keys.Left):
		m.focus = 0
	case key.Matches(msg, keys.Inspect):
		if p.sec != nil && len(p.keys) > 0 {
			m.modal = &inspectModal{key: p.selectedKey()}
		}
	}
	return nil
}

func (m *Model) reveal(all bool) tea.Cmd {
	p := &m.sec
	if p.sec == nil || len(p.keys) == 0 {
		return nil
	}
	if all {
		on := len(p.revealed) < len(p.keys)
		p.revealed = map[string]bool{}
		for _, k := range p.keys {
			p.revealed[k] = on
		}
	} else {
		k := p.selectedKey()
		p.revealed[k] = !p.revealed[k]
	}
	p.revealSeq++
	seq := p.revealSeq
	return tea.Tick(time.Duration(m.Config.UI.RevealTimeoutSeconds)*time.Second, func(time.Time) tea.Msg { return remaskMsg{seq} })
}

func (m *Model) refresh() tea.Cmd {
	n := m.tree.selected()
	if m.focus == 1 && m.sec.path != "" {
		return m.openSecret(n, m.sec.path)
	}
	if n == nil {
		return nil
	}
	if !n.isDir {
		n = n.parent
	}
	if n == nil {
		return nil
	}
	n.locked, n.loaded, n.expanded = false, false, true
	m.tree.rebuild()
	return m.loadChildren(n)
}

// --- view ---

// Layout rows (used by the mouse handler): header, then the panel's top border.
const (
	treeTop   = 2
	secretTop = 2 + secretHeaderRows
)

func (m *Model) leftWidth() int { return max(30, m.width*3/10) }

func (m *Model) View() string {
	if m.width == 0 {
		return ""
	}
	if m.fatal != nil {
		return "\n  " + stErr.Render("✗ "+m.fatal.Error()) + "\n\n  " + stDim.Render("Press q to quit.")
	}
	paneH := max(m.height-3, 5)
	lw := m.leftWidth()
	rw := m.width - lw
	modal := m.modal != nil

	left := panel(m.tree.title(lw-8, len(m.indexPaths)),
		m.tree.view(lw-4, paneH-2, m.sec.path, m.isFav, m.focus == 0 && !modal, m.spin.View()),
		lw, paneH, m.focus == 0 && !modal)
	var right string
	if m.sec.path == "" {
		right = panel(stTitle.Render("Welcome"), m.welcome(rw-4, paneH-2), rw, paneH, m.focus == 1 && !modal)
	} else {
		right = panel(breadcrumb(m.sec.path), m.sec.view(rw-4, paneH-2, m.focus == 1 && !modal, m.isFav(m.sec.path)),
			rw, paneH, m.focus == 1 && !modal)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	if modal {
		body = overlay(body, m.modal.view(m, m.width-8, paneH-2), m.width, paneH)
	}
	return strings.Join([]string{m.header(), body, m.statusLine(), m.hints()}, "\n")
}

func (m *Model) isFav(p string) bool { return m.Config.FavoriteIndex(p) >= 0 }

// spread puts left and right on one line of width w.
func spread(left, right string, w int) string {
	gap := w - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		return ansi.Truncate(left, max(w-ansi.StringWidth(right)-1, 0), "…") + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m *Model) header() string {
	host := strings.TrimPrefix(strings.TrimPrefix(m.Addr, "https://"), "http://")
	left := solid("◈ vaultui", cAccent) + " "
	if m.VaultName != "" {
		left += pill(m.VaultName, cPurple) + " "
	}
	if !strings.HasPrefix(host, m.VaultName+":") && host != m.VaultName {
		left += " " + stMuted.Render(host)
	}
	left += stDim.Render("  V switch")
	var right []string
	if m.inflight > 0 || m.indexing {
		s := m.spin.View()
		if m.indexing {
			s += " indexing " + thousands(len(m.indexPaths))
		}
		right = append(right, stDim.Render(s))
	}
	if m.Info != nil {
		right = append(right, pill("● "+m.Info.DisplayName+stDim.Background(cSurface).Render(" via "+m.Info.Method), cText))
		if !m.Info.Expire.IsZero() {
			ttl := m.Info.TTL()
			c := cGreen
			switch {
			case ttl < reloginWarn:
				c = cRed
			case ttl < time.Hour:
				c = cYellow
			}
			t := "⏱ " + fmtTTL(ttl)
			if ttl < reloginWarn && m.Relogin != nil {
				t += " · L re-login"
			}
			right = append(right, pill(t, c))
		}
	}
	return spread(left, strings.Join(right, " "), m.width)
}

// mode is the vim-style mode pill shown at the left of the status line.
func (m *Model) mode() (string, lipgloss.TerminalColor) {
	switch {
	case m.cmdline != nil:
		return "COMMAND", cGreen
	case m.tree.search != nil || m.sec.search != nil:
		return "FIND", cYellow
	}
	switch m.modal.(type) {
	case nil:
		return "NORMAL", cAccent
	case *searchModal:
		return "SEARCH", cPurple
	case *diffModal:
		return "DIFF", cPurple
	case *versionsModal:
		return "VERSIONS", cPurple
	case *inspectModal:
		return "INSPECT", cPurple
	case *favoritesModal:
		return "FAVORITES", cPurple
	case *exportModal:
		return "EXPORT", cYellow
	case *copyAsModal:
		return "COPY AS", cGreen
	}
	return "HELP", cPurple
}

func (m *Model) statusLine() string {
	name, c := m.mode()
	left := solid(name, c) + " "
	if m.cmdline != nil {
		m.cmdline.Width = m.width - ansi.StringWidth(left) - 2
		return left + m.cmdline.View()
	}
	where := "no secret open"
	if m.sec.path != "" {
		where = m.sec.path
	}
	left += pill(where, nil)
	switch {
	case m.toast == "":
	case m.toastErr:
		left += "  " + stErr.Render("✗ "+m.toast)
	default:
		left += "  " + stToast.Render("✓ "+m.toast)
	}
	right := ""
	if sc := m.vim.showcmd(); sc != "" {
		right = stWarnBold.Render(sc) + " "
	}
	right += pill(plural(len(m.indexPaths), "secret")+" indexed", nil)
	return spread(left, right, m.width)
}

// hints renders the contextual key list: accent key, dim description.
func (m *Model) hints() string {
	var parts []string
	for _, b := range m.footerKeys() {
		h := b.Help()
		parts = append(parts, stKeyCap.Render(h.Key)+" "+stDim.Render(h.Desc))
	}
	return ansi.Truncate(" "+strings.Join(parts, stDim.Faint(true).Render("  ·  ")), m.width, "…")
}

var logo = []string{
	"█ █ ▄▀█ █ █ █   ▀█▀ █ █ █",
	"▀▄▀ █▀█ █▄█ █▄▄  █  █▄█ █",
}

// welcome fills the right pane until a secret is opened.
func (m *Model) welcome(w, h int) string {
	from, to := "#7AA2F7", "#BB9AF7"
	if !lipgloss.HasDarkBackground() {
		from, to = "#2E7DE9", "#9854F1"
	}
	var lines []string
	for _, l := range logo {
		lines = append(lines, gradient(l, from, to))
	}
	lines = append(lines, stMuted.Render("read-only explorer for HashiCorp Vault"), "")

	card := func(n, label string, c lipgloss.TerminalColor) string {
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cBorder).Padding(0, 2).Width(16).
			Render(lipgloss.NewStyle().Foreground(c).Bold(true).Render(n) + "\n" + stDim.Render(label))
	}
	cards := lipgloss.JoinHorizontal(lipgloss.Top,
		card(thousands(len(m.mounts)), "KV mounts", cAccent), " ",
		card(thousands(len(m.indexPaths)), "secrets", cAccent2), " ",
		card(thousands(len(m.Config.Favorites)), "favorites", cYellow))
	if w >= 56 {
		lines = append(lines, strings.Split(cards, "\n")...)
	} else {
		lines = append(lines, stDim.Render(fmt.Sprintf("%s · %s · %s", plural(len(m.mounts), "mount"),
			plural(len(m.indexPaths), "secret"), plural(len(m.Config.Favorites), "favorite"))))
	}
	lines = append(lines, "")

	key := func(k, d string) string { return pad(stKeyCap.Render(k), 9) + stMuted.Render(d) }
	grid := [][2]string{
		{key("ctrl+p", "search everything"), key("/", "find in this pane")},
		{key("enter", "open"), key("tab", "switch pane")},
		{key("c", "copy value"), key("d", "diff")},
		{key(":", "command"), key("?", "all keys")},
	}
	lines = append(lines, stSection.Render("Quick keys"))
	for _, r := range grid {
		lines = append(lines, pad(r[0], 30)+r[1])
	}
	lines = append(lines, "")

	if len(m.Config.Favorites) > 0 {
		lines = append(lines, stSection.Render("★ Favorites"))
		for i, f := range m.Config.Favorites {
			if i == 9 {
				break
			}
			label := f.Alias
			if label == "" {
				label = f.Path
			}
			line := pad(stKeyCap.Render(fmt.Sprintf("'%d", i+1)), 5) + stText.Render(label)
			if f.Alias != "" {
				line += "  " + stDim.Render(f.Path)
			}
			lines = append(lines, line)
		}
		lines = append(lines, "")
	} else {
		lines = append(lines, stDim.Render("Tip: press f on a secret to favorite it, then '1–'9 to jump back."), "")
	}
	if len(m.Config.Recent) > 0 {
		lines = append(lines, stSection.Render("↺ Recent"))
		for i, p := range m.Config.Recent {
			if i == 5 {
				break
			}
			lines = append(lines, "     "+breadcrumb(p))
		}
	}
	if len(lines) > h {
		lines = lines[:h]
	}
	block := lipgloss.NewStyle().Render(strings.Join(lines, "\n"))
	bw := lipgloss.Width(block)
	if bw >= w {
		return block
	}
	padL := strings.Repeat(" ", (w-bw)/2)
	out := strings.Split(block, "\n")
	for i := range out {
		out[i] = padL + out[i]
	}
	top := max((h-len(out))/3, 0)
	return strings.Repeat("\n", top) + strings.Join(out, "\n")
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.modal != nil {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			return m.modal.update(m, tea.KeyMsg{Type: tea.KeyUp})
		case tea.MouseButtonWheelDown:
			return m.modal.update(m, tea.KeyMsg{Type: tea.KeyDown})
		}
		return nil
	}
	inTree := msg.X < m.leftWidth()
	switch msg.Button {
	case tea.MouseButtonWheelUp, tea.MouseButtonWheelDown:
		k := tea.KeyMsg{Type: tea.KeyDown}
		if msg.Button == tea.MouseButtonWheelUp {
			k.Type = tea.KeyUp
		}
		if inTree {
			return m.treeKey(k)
		}
		return m.secretKey(k)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return nil
		}
		if inTree {
			m.focus = 0
			i := m.tree.offset + msg.Y - treeTop
			if msg.Y < treeTop || i >= len(m.tree.visible) {
				return nil
			}
			m.tree.cursor = i // click selects and opens/toggles
			return m.treeKey(tea.KeyMsg{Type: tea.KeyEnter})
		}
		if m.sec.sec == nil {
			return nil
		}
		m.focus = 1
		i := m.sec.offset + msg.Y - secretTop
		if msg.Y >= secretTop && i < len(m.sec.keys) {
			if i == m.sec.cursor {
				m.modal = &inspectModal{key: m.sec.selectedKey()}
			}
			m.sec.cursor = i
		}
	}
	return nil
}

// openWeb opens the current secret (or folder) in Vault's web UI.
func (m *Model) openWeb() tea.Cmd {
	full := m.sec.path
	if n := m.tree.selected(); m.focus == 0 && n != nil {
		full = n.full()
	}
	mount, path, ok := vault.SplitPath(full, m.mounts)
	if !ok {
		return nil
	}
	v2 := true
	for _, mt := range m.mounts {
		if mt.Path == mount {
			v2 = mt.Version == 2
		}
	}
	u := webURL(m.Addr, mount, path, v2)
	browser.Stdout, browser.Stderr = io.Discard, io.Discard
	if err := browser.OpenURL(u); err != nil {
		m.copy("web UI link", u, false)
		return nil
	}
	m.flash("Opened "+u, false)
	return nil
}

// webURL follows the Vault ≥1.15 UI routes.
// TODO(plan): older Vault UIs use /ui/vault/secrets/<mount>/show/<path> for v2 too.
func webURL(addr, mount, path string, v2 bool) string {
	base := strings.TrimSuffix(addr, "/") + "/ui/vault/secrets/" + url.PathEscape(strings.TrimSuffix(mount, "/"))
	isDir := path == "" || strings.HasSuffix(path, "/")
	switch {
	case v2 && isDir:
		return base + "/kv/list/" + path
	case v2:
		return base + "/kv/" + url.PathEscape(path) + "/details"
	case isDir:
		return base + "/list/" + path
	}
	return base + "/show/" + path
}

func fmtTTL(d time.Duration) string {
	if d <= 0 {
		return "expired"
	}
	h := int(d.Hours())
	mnt := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, mnt)
	}
	return fmt.Sprintf("%dm%02ds", mnt, int(d.Seconds())%60)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return thousands(n) + " " + word + "s"
}

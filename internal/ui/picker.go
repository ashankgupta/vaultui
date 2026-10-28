package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"vaultui/internal/config"
)

// Picker is the start screen: choose, add, edit or delete saved Vaults.
// After it quits, Chosen holds the selected Vault's name ("" = quit).
type Picker struct {
	cfg        *config.Config
	cursor     int // len(Vaults) = the "+ Add a Vault" row
	form       []textinput.Model
	field      int
	editIdx    int // -1 when adding
	confirmDel bool
	err        string
	width      int
	height     int

	Chosen string
	// Err is shown above the list, e.g. why connecting to a Vault failed.
	Err string
}

const (
	pfAddr = iota
	pfName
	pfNamespace
	pfMount
	pfRole
)

var pickerLabels = []string{"URL", "Name", "Namespace", "SSO mount", "SSO role"}

func NewPicker(cfg *config.Config) *Picker {
	applyTheme(cfg.UI.Theme)
	p := &Picker{cfg: cfg}
	for i, v := range cfg.Vaults {
		if v.Name == cfg.LastVault {
			p.cursor = i
		}
	}
	if len(cfg.Vaults) == 0 {
		p.openForm(-1)
	}
	return p
}

func (p *Picker) Init() tea.Cmd { return textinput.Blink }

func (p *Picker) openForm(idx int) {
	p.editIdx, p.field, p.err = idx, 0, ""
	v := config.Vault{}
	if idx >= 0 {
		v = p.cfg.Vaults[idx]
	}
	vals := []string{v.Addr, v.Name, v.Namespace, v.AuthMount, v.AuthRole}
	ph := []string{"https://vault.company.com", "e.g. prod (optional; defaults to the host name)", "optional (Vault Enterprise)",
		p.cfg.Auth.Mount + " (default)", "optional; empty uses the mount's default role"}
	p.form = make([]textinput.Model, len(vals))
	for i := range vals {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = ph[i]
		in.SetValue(vals[i])
		p.form[i] = in
	}
	p.field = pfAddr // first, and the only required field
	p.form[p.field].Focus()
}

func (p *Picker) focus(i int) {
	p.form[p.field].Blur()
	p.field = (i + len(p.form)) % len(p.form)
	p.form[p.field].Focus()
}

func (p *Picker) save() tea.Cmd {
	v := config.Vault{
		Name: p.form[pfName].Value(), Addr: p.form[pfAddr].Value(), Namespace: strings.TrimSpace(p.form[pfNamespace].Value()),
		AuthMount: strings.Trim(strings.TrimSpace(p.form[pfMount].Value()), "/"), AuthRole: strings.TrimSpace(p.form[pfRole].Value()),
	}
	first := len(p.cfg.Vaults) == 0
	if err := p.cfg.PutVault(p.editIdx, v); err != nil {
		p.err = err.Error()
		return nil
	}
	if err := p.cfg.Save(); err != nil {
		p.err = "saving config: " + err.Error()
		return nil
	}
	if p.editIdx < 0 {
		p.cursor = len(p.cfg.Vaults) - 1
	}
	p.form = nil
	if first { // nothing to choose between yet: connect straight away
		p.Chosen = p.cfg.Vaults[p.cursor].Name
		return tea.Quit
	}
	return nil
}

func (p *Picker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
		return p, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return p, tea.Quit
		}
		if p.form != nil {
			return p, p.formKey(msg)
		}
		return p, p.listKey(msg)
	}
	return p, nil
}

func (p *Picker) formKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		if len(p.cfg.Vaults) == 0 {
			return tea.Quit
		}
		p.form = nil
		return nil
	case "enter", "ctrl+s":
		return p.save()
	case "tab", "down":
		p.focus(p.field + 1)
		return nil
	case "shift+tab", "up":
		p.focus(p.field - 1)
		return nil
	}
	var cmd tea.Cmd
	p.form[p.field], cmd = p.form[p.field].Update(msg)
	p.err = ""
	return cmd
}

func (p *Picker) listKey(msg tea.KeyMsg) tea.Cmd {
	n := len(p.cfg.Vaults)
	if p.confirmDel {
		p.confirmDel = false
		if msg.String() == "y" && p.cursor < n {
			p.cfg.DeleteVault(p.cursor)
			p.cfg.Save()
			p.cursor = clamp(p.cursor, 0, len(p.cfg.Vaults))
			if len(p.cfg.Vaults) == 0 {
				p.openForm(-1)
			}
		}
		return nil
	}
	switch msg.String() {
	case "q", "esc":
		return tea.Quit
	case "up", "k":
		p.cursor = max(p.cursor-1, 0)
	case "down", "j", "tab":
		p.cursor = min(p.cursor+1, n)
	case "g", "home":
		p.cursor = 0
	case "G", "end":
		p.cursor = n
	case "a", "n", "+":
		p.openForm(-1)
		return textinput.Blink
	case "e":
		if p.cursor < n {
			p.openForm(p.cursor)
			return textinput.Blink
		}
	case "d", "x", "delete":
		if p.cursor < n {
			p.confirmDel = true
		}
	case "enter", "l", "right":
		if p.cursor == n {
			p.openForm(-1)
			return textinput.Blink
		}
		p.Chosen = p.cfg.Vaults[p.cursor].Name
		return tea.Quit
	default:
		if k := msg.String(); len(k) == 1 && k[0] >= '1' && k[0] <= '9' && int(k[0]-'1') < n {
			p.Chosen = p.cfg.Vaults[k[0]-'1'].Name
			return tea.Quit
		}
	}
	return nil
}

func (p *Picker) View() string {
	if p.width == 0 {
		return ""
	}
	w := min(p.width-4, 84)
	from, to := "#7AA2F7", "#BB9AF7"
	if !lipgloss.HasDarkBackground() {
		from, to = "#2E7DE9", "#9854F1"
	}
	var out []string
	for _, l := range logo {
		out = append(out, gradient(l, from, to))
	}
	out = append(out, stMuted.Render("read-only explorer for HashiCorp Vault"), "")

	var hint string
	if p.form != nil {
		out = append(out, p.formView(w))
		hint = "tab next field · enter save" + map[bool]string{true: " & connect", false: ""}[len(p.cfg.Vaults) == 0] + " · esc " +
			map[bool]string{true: "quit", false: "back"}[len(p.cfg.Vaults) == 0]
	} else {
		if p.Err != "" {
			out = append(out, stErr.Render(ansi.Truncate("✗ "+strings.ReplaceAll(p.Err, "\n", " "), w, "…")), "")
		}
		out = append(out, p.listView(w))
		hint = "enter connect · 1-9 quick pick · a add · e edit · d delete · q quit"
	}
	out = append(out, "", stDim.Render(hint))
	return lipgloss.Place(p.width, p.height, lipgloss.Center, lipgloss.Center, strings.Join(out, "\n"))
}

func (p *Picker) listView(w int) string {
	inner := w - 5 // panel padding + the selection bar column
	nw := 8
	for _, v := range p.cfg.Vaults {
		nw = max(nw, min(len(v.Name), 20))
	}
	var lines []string
	for i, v := range p.cfg.Vaults {
		var tags []string
		if v.Name == p.cfg.LastVault {
			tags = append(tags, "last used")
		}
		if v.Namespace != "" {
			tags = append(tags, "ns "+v.Namespace)
		}
		if v.AuthMount != "" {
			tags = append(tags, "sso "+v.AuthMount)
		}
		num := stKeyCap.Render(string(rune('1'+i))) + " "
		if i >= 9 {
			num = "  "
		}
		line := num + pad(stTitle.Render(ansi.Truncate(v.Name, nw, "…")), nw) + "  " + stMuted.Render(v.Addr)
		line = spread(ansi.Truncate(line, inner-18, "…"), stDim.Render(strings.Join(tags, " · ")), inner)
		lines = append(lines, p.row(line, i == p.cursor, inner))
	}
	add := stKeyCap.Render("+") + " " + stMuted.Render("Add a Vault")
	lines = append(lines, p.row(add, p.cursor == len(p.cfg.Vaults), inner))
	if p.confirmDel {
		lines = append(lines, "", stErr.Render("Delete "+p.cfg.Vaults[p.cursor].Name+"? Its favorites go too. y to confirm, any key to cancel."))
	}
	return panel(stTitle.Render("Choose a Vault"), strings.Join(lines, "\n"), w, 0, true)
}

func (p *Picker) row(s string, sel bool, w int) string {
	if sel {
		return lipgloss.NewStyle().Foreground(cAccent).Background(cSelBg).Render("▌") + stSel.Render(pad(ansi.Strip(s), w))
	}
	return " " + s
}

func (p *Picker) formView(w int) string {
	inner := w - 4
	title := "Add a Vault"
	if p.editIdx >= 0 {
		title = "Edit " + p.cfg.Vaults[p.editIdx].Name
	}
	lines := []string{""}
	for i, in := range p.form {
		in.Width = inner - 16
		var label string
		if i == p.field {
			label = pad(stKeyCap.Render("▸ "+pickerLabels[i]), 12)
		} else {
			label = "  " + pad(stMuted.Render(pickerLabels[i]), 10)
		}
		lines = append(lines, label+"  "+in.View())
		if i == pfAddr {
			lines = append(lines, strings.Repeat(" ", 14)+stDim.Render("paste the address you log in at; web UI links work too"))
		}
	}
	lines = append(lines, "")
	if p.err != "" {
		lines = append(lines, stErr.Render("✗ "+p.err))
	} else {
		lines = append(lines, stDim.Render("Saved in "+configHint+" · each Vault keeps its own login (0600)"))
	}
	return panel(stTitle.Render(title), strings.Join(lines, "\n"), w, 0, true)
}

const configHint = "~/.config/vaultui/config.yaml"

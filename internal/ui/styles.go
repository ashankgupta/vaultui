package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type ac = lipgloss.AdaptiveColor

// Tokyo Night (dark) / Tokyo Night Day (light).
var (
	cText    = ac{Light: "#343B58", Dark: "#C0CAF5"}
	cMuted   = ac{Light: "#4C505E", Dark: "#A9B1D6"}
	cDim     = ac{Light: "#8990B3", Dark: "#565F89"}
	cBorder  = ac{Light: "#C4C8DA", Dark: "#3B4261"}
	cAccent  = ac{Light: "#2E7DE9", Dark: "#7AA2F7"}
	cAccent2 = ac{Light: "#007197", Dark: "#7DCFFF"}
	cPurple  = ac{Light: "#9854F1", Dark: "#BB9AF7"}
	cGreen   = ac{Light: "#587539", Dark: "#9ECE6A"}
	cRed     = ac{Light: "#F52A65", Dark: "#F7768E"}
	cYellow  = ac{Light: "#8C6C3E", Dark: "#E0AF68"}
	cSurface = ac{Light: "#E1E2E7", Dark: "#292E42"}
	cSelBg   = ac{Light: "#B7C1E3", Dark: "#283457"}
	cOnColor = ac{Light: "#FFFFFF", Dark: "#1A1B26"}

	stText     = lipgloss.NewStyle().Foreground(cText)
	stMuted    = lipgloss.NewStyle().Foreground(cMuted)
	stDim      = lipgloss.NewStyle().Foreground(cDim)
	stHeader   = lipgloss.NewStyle().Bold(true).Foreground(cAccent)
	stTitle    = lipgloss.NewStyle().Bold(true).Foreground(cText)
	stFolder   = lipgloss.NewStyle().Foreground(cText)
	stValue    = lipgloss.NewStyle().Foreground(cGreen)
	stSel      = lipgloss.NewStyle().Background(cSelBg).Foreground(cText).Bold(true)
	stSelBlur  = lipgloss.NewStyle().Bold(true)
	stAdded    = lipgloss.NewStyle().Foreground(cGreen)
	stRemoved  = lipgloss.NewStyle().Foreground(cRed)
	stChanged  = lipgloss.NewStyle().Foreground(cYellow)
	stErr      = lipgloss.NewStyle().Foreground(cRed).Bold(true)
	stMatch    = lipgloss.NewStyle().Foreground(cYellow).Bold(true).Underline(true)
	stStar     = lipgloss.NewStyle().Foreground(cYellow)
	stToast    = lipgloss.NewStyle().Foreground(cGreen)
	stWarnBold = lipgloss.NewStyle().Foreground(cYellow).Bold(true)
	stSection  = lipgloss.NewStyle().Foreground(cPurple).Bold(true)
	stKeyCap   = lipgloss.NewStyle().Foreground(cAccent).Bold(true)
)

// applyTheme forces dark/light; "auto" lets lipgloss detect the terminal.
func applyTheme(theme string) {
	switch theme {
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	case "light":
		lipgloss.SetHasDarkBackground(false)
	}
}

const mask = "••••••••"

// pill is a padded label on a surface background; fg colors the text.
func pill(s string, fg lipgloss.TerminalColor) string {
	st := lipgloss.NewStyle().Background(cSurface).Foreground(cMuted).Padding(0, 1)
	if fg != nil {
		st = st.Foreground(fg).Bold(true)
	}
	return st.Render(s)
}

// solid is a pill with a colored background (mode indicators, logo).
func solid(s string, bg lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Background(bg).Foreground(cOnColor).Bold(true).Padding(0, 1).Render(s)
}

// badge is kept for older call sites.
func badge(s string, fg lipgloss.TerminalColor) string { return pill(s, fg) }

// panel draws a rounded box w×h (h<=0: fit content) with the title set into
// the top border, and one column of padding inside.
func panel(title, body string, w, h int, focused bool) string {
	bc := cBorder
	if focused {
		bc = cAccent
	}
	b := lipgloss.NewStyle().Foreground(bc)
	inner := max(w-4, 1)
	t := ""
	if title != "" {
		t = " " + ansi.Truncate(title, max(w-8, 1), "…") + " "
	}
	top := b.Render("╭─") + t + b.Render(strings.Repeat("─", max(w-3-ansi.StringWidth(t), 0))+"╮")
	lines := strings.Split(body, "\n")
	if h > 0 {
		for len(lines) < h-2 {
			lines = append(lines, "")
		}
		lines = lines[:max(h-2, 0)]
	}
	out := []string{top}
	for _, l := range lines {
		out = append(out, b.Render("│")+" "+pad(ansi.Truncate(l, inner, "…"), inner)+" "+b.Render("│"))
	}
	out = append(out, b.Render("╰"+strings.Repeat("─", max(w-2, 0))+"╯"))
	return strings.Join(out, "\n")
}

// modalBox is the frame every pop-up uses: title in the border, accent color.
func modalBox(title string, lines []string, w int) string {
	return panel(stTitle.Render(title), strings.Join(lines, "\n"), w+4, 0, true)
}

// titled frames a pop-up whose first line is its title.
func titled(lines []string, w int) string {
	return panel(lines[0], strings.Join(lines[1:], "\n"), w+4, 0, true)
}

// gradient colors each rune of s from color a to b (hex), for the logo.
func gradient(s, a, b string) string {
	rs := []rune(s)
	var out strings.Builder
	for i, r := range rs {
		if r == ' ' {
			out.WriteRune(r)
			continue
		}
		t := float64(i) / float64(max(len(rs)-1, 1))
		out.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(lerpHex(a, b, t))).Render(string(r)))
	}
	return out.String()
}

func lerpHex(a, b string, t float64) string {
	p := func(h string, i int) float64 { v, _ := strconv.ParseUint(h[1+2*i:3+2*i], 16, 8); return float64(v) }
	var s strings.Builder
	s.WriteByte('#')
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&s, "%02X", int(p(a, i)+(p(b, i)-p(a, i))*t))
	}
	return s.String()
}

// ago renders t relative to now: "just now", "5m ago", "3d ago", or a date.
func ago(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Local().Format("2006-01-02")
}

// overlay draws fg centered on top of a dimmed copy of bg.
func overlay(bg, fg string, w, h int) string {
	bl := strings.Split(bg, "\n")
	for len(bl) < h {
		bl = append(bl, "")
	}
	for i, l := range bl {
		bl[i] = lipgloss.NewStyle().Foreground(cBorder).Render(pad(ansi.Strip(l), w))
	}
	fl := strings.Split(fg, "\n")
	fw := 0
	for _, l := range fl {
		fw = max(fw, ansi.StringWidth(l))
	}
	x, y := max((w-fw)/2, 0), max((h-len(fl))/2, 0)
	for i, l := range fl {
		if y+i >= len(bl) {
			break
		}
		b := bl[y+i]
		bl[y+i] = ansi.Truncate(b, x, "") + pad(l, fw) + ansi.TruncateLeft(b, x+fw, "")
	}
	return strings.Join(bl[:h], "\n")
}

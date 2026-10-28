package ui

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// inspectModal shows one value in full: pretty JSON, decoded JWT claims,
// certificate details. It shares the secret pane's reveal state, so the
// usual re-mask timeout applies.
type inspectModal struct {
	key    string
	offset int
}

func (v *inspectModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	if c, ok := m.nav(msg, v.offset, 1<<20); ok { // clamped in view
		v.offset = c
		return nil
	}
	switch msg.String() {
	case "esc", "q", "enter":
		m.modal = nil
	case " ":
		v.offset += m.halfPage()
	case "r":
		return m.reveal(false)
	case "c":
		m.copy(v.key, m.sec.sec.Data[v.key], true)
	}
	return nil
}

func (v *inspectModal) view(m *Model, w, h int) string {
	w = min(w, 110)
	val := m.sec.sec.Data[v.key]
	kind, details, body := describe(val)
	shown := m.sec.revealed[v.key]

	head := []string{
		stTitle.Render(v.key) + "  " + badge(kind, cAccent) + " " +
			badge(plural(len([]rune(val)), "char"), nil) + " " +
			badge(plural(strings.Count(val, "\n")+1, "line"), nil),
	}
	if shown {
		head = append(head, details...)
	}
	head = append(head, stDim.Render(strings.Repeat("─", w)))

	rows := max(h-len(head)-3, 3)
	var lines []string
	if !shown {
		lines = []string{"", stDim.Render(mask) + "   " + stDim.Render("press ") + stKeyCap.Render("r") + stDim.Render(" to reveal")}
	} else {
		v.offset = clamp(v.offset, 0, max(len(body)-rows, 0))
		nw := len(fmt.Sprint(len(body)))
		for i := v.offset; i < len(body) && i < v.offset+rows; i++ {
			lines = append(lines, stDim.Render(fmt.Sprintf("%*d ", nw, i+1))+ansi.Truncate(stValue.Render(body[i]), w-nw-1, "…"))
		}
	}
	foot := stDim.Render("r reveal · c copy · j/k ctrl+d/u gg/G scroll · esc close")
	if shown && len(body) > rows {
		foot = stDim.Render(fmt.Sprintf("lines %d-%d of %d · ", v.offset+1, min(v.offset+rows, len(body)), len(body))) + foot
	}
	all := append(append(head, lines...), "", foot)
	return titled(all, w)
}

// describe classifies a value and returns extra detail lines (shown only
// when revealed) plus the body to display.
func describe(v string) (kind string, details, body []string) {
	t := strings.TrimSpace(v)
	if p := strings.Split(t, "."); len(p) == 3 && strings.HasPrefix(t, "eyJ") {
		hdr, err1 := base64.RawURLEncoding.DecodeString(p[0])
		pl, err2 := base64.RawURLEncoding.DecodeString(p[1])
		if err1 == nil && err2 == nil {
			var claims map[string]any
			json.Unmarshal(pl, &claims)
			for _, c := range []string{"iat", "nbf", "exp"} {
				if f, ok := claims[c].(float64); ok {
					tm := time.Unix(int64(f), 0)
					s := fmt.Sprintf("%s %s (%s)", c, tm.Local().Format("2006-01-02 15:04"), relTime(tm))
					if c == "exp" && tm.Before(time.Now()) {
						s = stErr.Render(s + " · EXPIRED")
					}
					details = append(details, stDim.Render(s))
				}
			}
			body = append(append([]string{"// header"}, pretty(hdr)...), "", "// payload")
			body = append(body, pretty(pl)...)
			return "JWT", details, append(body, "", "// signature not shown")
		}
	}
	if (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		return "JSON", nil, pretty([]byte(t))
	}
	if blk, _ := pem.Decode([]byte(t)); blk != nil {
		kind = "PEM " + strings.ToLower(blk.Type)
		if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
			exp := fmt.Sprintf("expires %s (%s)", c.NotAfter.Local().Format("2006-01-02"), relTime(c.NotAfter))
			if c.NotAfter.Before(time.Now()) {
				exp = stErr.Render(exp + " · EXPIRED")
			} else if time.Until(c.NotAfter) < 30*24*time.Hour {
				exp = stWarnBold.Render(exp)
			}
			details = []string{stDim.Render("subject " + c.Subject.String()), stDim.Render("issuer  " + c.Issuer.String()), exp}
			if len(c.DNSNames) > 0 {
				details = append(details, stDim.Render("dns     "+strings.Join(c.DNSNames, ", ")))
			}
			kind = "certificate"
		}
	} else if strings.Contains(v, "\n") {
		kind = "multi-line"
	} else {
		kind = "text"
	}
	return kind, details, strings.Split(v, "\n")
}

func pretty(b []byte) []string {
	var out bytes.Buffer
	if json.Indent(&out, b, "", "  ") != nil {
		return strings.Split(string(b), "\n")
	}
	return strings.Split(out.String(), "\n")
}

// relTime is like ago but also handles the future: "in 23d".
func relTime(t time.Time) string {
	if t.Before(time.Now()) {
		return ago(t)
	}
	d := time.Until(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	}
	return fmt.Sprintf("in %dd", int(d.Hours()/24))
}

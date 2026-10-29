package ui

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/muesli/termenv"

	"vaultui/internal/auth"
	"vaultui/internal/config"
	"vaultui/internal/vault"
)

// Update snapshots with: go test ./internal/ui -update

type fakeStore struct{}

var fakeData = map[string]map[string]string{
	"secret/payments/prod/db":    {"DB_HOST": "db.prod", "DB_PASSWORD": "pw-prod", "POOL": "10"},
	"secret/payments/staging/db": {"DB_HOST": "db.staging", "DB_PASSWORD": "pw-staging", "EXTRA": "x"},
	"kv-v1/legacy":               {"TOKEN": "old"},
}

func (fakeStore) Mounts(context.Context) ([]vault.Mount, error) {
	return []vault.Mount{{Path: "kv-v1/", Version: 1}, {Path: "secret/", Version: 2}}, nil
}

func (fakeStore) List(_ context.Context, mount, path string) ([]vault.Entry, error) {
	switch mount + path {
	case "secret/":
		return []vault.Entry{{Name: "locked/", IsDir: true}, {Name: "payments/", IsDir: true}}, nil
	case "secret/locked/":
		return nil, vault.ErrForbidden
	case "secret/payments/":
		return []vault.Entry{{Name: "prod/", IsDir: true}, {Name: "staging/", IsDir: true}}, nil
	case "secret/payments/prod/", "secret/payments/staging/":
		return []vault.Entry{{Name: "db"}}, nil
	case "kv-v1/":
		return []vault.Entry{{Name: "legacy"}}, nil
	}
	return nil, nil
}

func (fakeStore) Read(_ context.Context, mount, path string, version int) (*vault.Secret, error) {
	d, ok := fakeData[mount+path]
	if !ok {
		return nil, vault.ErrNotFound
	}
	s := &vault.Secret{Mount: mount, Path: path, KVVersion: 1, Data: d}
	if mount == "secret/" {
		s.KVVersion, s.Version, s.Created = 2, 3, time.Now().Add(-49*time.Hour)
	}
	return s, nil
}

func (fakeStore) Versions(context.Context, string, string) ([]vault.VersionMeta, error) {
	return []vault.VersionMeta{{Version: 3}, {Version: 2, Deleted: true}, {Version: 1}}, nil
}

func start(t *testing.T) *teatest.TestModel {
	lipgloss.SetColorProfile(termenv.Ascii)
	cfg, err := config.Load(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{Store: fakeStore{}, Config: cfg, Info: &auth.Info{DisplayName: "ashank", Method: "oidc"}, Addr: "https://vault.test"})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(100, 18))
	waitFor(t, tm, "secret/")
	return tm
}

func waitFor(t *testing.T, tm *teatest.TestModel, s string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte(s)) },
		teatest.WithDuration(3*time.Second), teatest.WithCheckInterval(10*time.Millisecond))
}

func keys_(tm *teatest.TestModel, ks ...string) {
	for _, k := range ks {
		switch k {
		case "enter":
			tm.Send(tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
		case "down":
			tm.Send(tea.KeyMsg{Type: tea.KeyDown})
		case "ctrl+p":
			tm.Send(tea.KeyMsg{Type: tea.KeyCtrlP})
		default:
			tm.Type(k)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func finish(t *testing.T, tm *teatest.TestModel) {
	t.Helper()
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	m := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	golden.RequireEqual(t, []byte(m.View()))
}

func TestBrowseAndReveal(t *testing.T) {
	tm := start(t)
	// search jump expands parents, then reveal one value
	keys_(tm, "ctrl+p", "payproddb")
	waitFor(t, tm, "secret/payments/prod/db")
	keys_(tm, "enter")
	waitFor(t, tm, "DB_PASSWORD")
	keys_(tm, "j", "r")
	waitFor(t, tm, "pw-prod")
	finish(t, tm)
}

func TestLockedFolder(t *testing.T) {
	tm := start(t)
	keys_(tm, "j", "enter")
	waitFor(t, tm, "locked/")
	keys_(tm, "j", "enter")
	waitFor(t, tm, "🔒")
	finish(t, tm)
}

func TestEnvDiff(t *testing.T) {
	tm := start(t)
	keys_(tm, "ctrl+p", "payproddb")
	waitFor(t, tm, "secret/payments/prod/db")
	keys_(tm, "enter")
	waitFor(t, tm, "DB_PASSWORD")
	keys_(tm, "d")
	waitFor(t, tm, "suggestions")
	keys_(tm, "enter")
	waitFor(t, tm, "changed")
	keys_(tm, "r")
	finish(t, tm)
}

func TestVersionsKVv1(t *testing.T) {
	tm := start(t)
	keys_(tm, "enter")
	waitFor(t, tm, "legacy")
	keys_(tm, "j", "enter")
	waitFor(t, tm, "TOKEN")
	keys_(tm, "v")
	waitFor(t, tm, "not available for KV v1")
	m := tm
	finish(t, m)
}

func TestHumanizers(t *testing.T) {
	if thousands(1240) != "1,240" || thousands(50000) != "50,000" || thousands(7) != "7" {
		t.Fatal("thousands")
	}
	if fmtTTL(7*time.Hour+42*time.Minute) != "7h42m" || !strings.HasPrefix(fmtTTL(-1), "expired") {
		t.Fatal("fmtTTL")
	}
}

func TestMouseAndInspect(t *testing.T) {
	tm := start(t)
	// click "secret/" (2nd tree row), then "payments/", "prod/", "db"
	for _, y := range []int{treeTop + 1, treeTop + 3, treeTop + 4, treeTop + 5} {
		tm.Send(tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, tm, "DB_PASSWORD")
	keys_(tm, "enter", "r")
	waitFor(t, tm, "chars")
	finish(t, tm)
}

func TestVimNavigation(t *testing.T) {
	tm := start(t)
	keys_(tm, "j", "z", "o")
	waitFor(t, tm, "payments/")
	keys_(tm, "G")                // last row: payments/
	keys_(tm, "g", "g", "3", "j") // top, then 3 down: payments/ again
	keys_(tm, "z", "a")
	waitFor(t, tm, "prod/")
	keys_(tm, ":")
	tm.Type("e secret/payments/staging/db")
	keys_(tm, "enter")
	waitFor(t, tm, "EXTRA")
	keys_(tm, "G") // secret pane is focused: last key
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	m := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	if m.sec.path != "secret/payments/staging/db" || m.sec.selectedKey() != "EXTRA" {
		t.Fatalf("path=%s key=%s", m.sec.path, m.sec.selectedKey())
	}
	if n := m.tree.selected(); n == nil || n.full() != "secret/payments/staging/db" {
		t.Fatalf("tree selection: %+v", n)
	}
}

func TestCmdlineCompletion(t *testing.T) {
	m := New(Options{Config: config.Default()})
	m.indexPaths = []string{"secret/payments/prod/db", "secret/payments/staging/db", "secret/infra/x"}
	in := textinput.New()
	m.cmdline = &in
	for _, c := range []struct{ in, want string }{
		{"e sec", "e secret/"},
		{"e secret/p", "e secret/payments/"},
		{"e secret/payments/s", "e secret/payments/staging/"},
		{"secret/i", "secret/infra/"},
		{"e nope", "e nope"},
	} {
		m.cmdline.SetValue(c.in)
		m.completeCmdline()
		if got := m.cmdline.Value(); got != c.want {
			t.Errorf("%q → %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSecretKeyFilter(t *testing.T) {
	tm := start(t)
	keys_(tm, ":")
	tm.Type("e secret/payments/prod/db")
	keys_(tm, "enter")
	waitFor(t, tm, "DB_PASSWORD")
	keys_(tm, "/") // secret pane is focused → filter keys, not global search
	tm.Type("pwd")
	waitFor(t, tm, "1 of 3")
	keys_(tm, "enter", "r")
	waitFor(t, tm, "pw-prod")
	finish(t, tm)
}

func TestTreeFind(t *testing.T) {
	tm := start(t)
	keys_(tm, "j", "l")
	waitFor(t, tm, "payments/")
	keys_(tm, "l", "l") // payments/ is the 2nd child: move down then expand
	keys_(tm, "g", "g", "/")
	tm.Type("pay")
	waitFor(t, tm, "1 match")
	keys_(tm, "enter", "l")
	waitFor(t, tm, "staging/")
	keys_(tm, "/")
	tm.Type("o")
	waitFor(t, tm, "matches") // locked/, prod/
	keys_(tm, "enter", "n")
	finish(t, tm)
}

func TestCopyAsPreviewIsMasked(t *testing.T) {
	tm := start(t)
	keys_(tm, ":")
	tm.Type("e secret/payments/prod/db")
	keys_(tm, "enter")
	waitFor(t, tm, "DB_PASSWORD")
	keys_(tm, "x", "j", "j") // Kubernetes Secret
	waitFor(t, tm, "stringData")
	tm.Send(tea.KeyMsg{Type: tea.KeyCtrlC})
	m := tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(*Model)
	v := m.View()
	if strings.Contains(v, "pw-prod") || !strings.Contains(v, "kind: Secret") {
		t.Fatalf("preview leaked or wrong:\n%s", v)
	}
}

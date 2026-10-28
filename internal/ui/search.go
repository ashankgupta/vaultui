package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"vaultui/internal/index"
)

const searchLimit = 50

// searchModal is the fuzzy finder. With onPick set it's a path picker (diff);
// otherwise enter jumps to the secret.
type searchModal struct {
	title   string
	input   textinput.Model
	empty   []string // shown when the query is empty (recent paths or suggestions)
	results []index.Match
	cursor  int
	onPick  func(m *Model, path string) tea.Cmd
}

func newSearch(m *Model, title string, empty []string, onPick func(*Model, string) tea.Cmd) *searchModal {
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "fuzzy search paths"
	in.Focus()
	s := &searchModal{title: title, input: in, empty: empty, onPick: onPick}
	s.refilter(m)
	return s
}

func (s *searchModal) refilter(m *Model) {
	q := s.input.Value()
	if q == "" {
		s.results = s.results[:0]
		for _, p := range s.empty {
			s.results = append(s.results, index.Match{Path: p})
		}
	} else {
		// TODO(plan): `k:` key-name search (index_keys) not implemented; it's the optional stretch goal.
		s.results = index.Search(q, m.indexPaths, searchLimit)
	}
	s.cursor = clamp(s.cursor, 0, max(len(s.results)-1, 0))
}

func (s *searchModal) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.modal = nil
		return nil
	case "up", "ctrl+p", "ctrl+k":
		s.cursor = max(s.cursor-1, 0)
		return nil
	case "down", "ctrl+n", "ctrl+j":
		s.cursor = min(s.cursor+1, max(len(s.results)-1, 0))
		return nil
	case "ctrl+r":
		return m.startCrawl()
	case "enter":
		if s.cursor >= len(s.results) {
			return nil
		}
		p := s.results[s.cursor].Path
		m.modal = nil
		if s.onPick != nil {
			return s.onPick(m, p)
		}
		if q := s.input.Value(); q != "" {
			m.hits, m.hitIdx, m.hitQuery = nil, s.cursor, q
			for _, r := range s.results {
				m.hits = append(m.hits, r.Path)
			}
		}
		m.focus = 1
		return m.jumpTo(p)
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	s.refilter(m)
	return cmd
}

func (s *searchModal) view(m *Model, w, h int) string {
	w = min(w, 100)
	s.input.Width = w - 4
	lines := []string{stTitle.Render(s.title), s.input.View()}
	status := fmt.Sprintf("%s paths indexed", thousands(len(m.indexPaths)))
	if m.indexing {
		status = fmt.Sprintf("indexing… %s paths", thousands(len(m.indexPaths)))
	}
	if s.input.Value() == "" && len(s.empty) > 0 {
		status = "recent"
		if s.onPick != nil {
			status = "suggestions"
		}
	}
	lines = append(lines, stDim.Render(status+" · enter select · esc close · ctrl+r reindex"))
	rows := h - len(lines) - 2
	for i := 0; i < len(s.results) && i < rows; i++ {
		line := highlight(s.results[i].Path, s.results[i].Indexes)
		line = ansi.Truncate(line, w, "…")
		if i == s.cursor {
			line = stSel.Render(pad(line, w))
		}
		lines = append(lines, line)
	}
	return titled(lines, w)
}

func highlight(s string, idx []int) string { return highlightIn(s, idx, lipgloss.NewStyle()) }

// highlightIn renders s in base, with the runes at idx in the match style.
func highlightIn(s string, idx []int, base lipgloss.Style) string {
	if len(idx) == 0 {
		return base.Render(s)
	}
	hit := map[int]bool{}
	for _, i := range idx {
		hit[i] = true
	}
	var b strings.Builder
	for i, r := range s {
		if hit[i] {
			b.WriteString(stMatch.Render(string(r)))
		} else {
			b.WriteString(base.Render(string(r)))
		}
	}
	return b.String()
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// --- index plumbing ---

type indexBatchMsg struct {
	gen   int
	paths []string
}
type indexDoneMsg struct{ gen int }

func (m *Model) loadIndexCache() {
	if m.CacheDir == "" {
		return
	}
	ix, err := index.Load(index.CachePath(m.CacheDir, m.Addr))
	if err != nil {
		return
	}
	m.indexPaths = ix.Paths
	m.indexUpdated = ix.Updated
}

// startCrawl (re)builds the index in the background. Results stream in as
// indexBatchMsg; the old index stays searchable until the crawl finishes.
func (m *Model) startCrawl() tea.Cmd {
	if m.crawlCancel != nil {
		m.crawlCancel()
	}
	m.crawlGen++
	gen := m.crawlGen
	ctx, cancel := context.WithCancel(context.Background())
	m.crawlCancel = cancel
	m.indexing = true
	m.crawlPaths = nil
	m.indexSet = map[string]bool{}
	for _, p := range m.indexPaths {
		m.indexSet[p] = true
	}
	ch := make(chan tea.Msg, 64)
	o := m.Config.Search
	mounts := m.mounts
	go func() {
		index.Crawl(ctx, m.Store, mounts, index.Options{Concurrency: o.Concurrency, MaxDepth: o.MaxDepth, MaxPaths: o.MaxPaths}, func(b []string) {
			select {
			case ch <- indexBatchMsg{gen, b}:
			case <-ctx.Done():
			}
		})
		select {
		case ch <- indexDoneMsg{gen}:
		case <-ctx.Done():
		}
	}()
	m.indexCh = ch
	return waitIndex(ch)
}

func waitIndex(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

func (m *Model) onIndexMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case indexBatchMsg:
		if msg.gen != m.crawlGen {
			return nil
		}
		m.crawlPaths = append(m.crawlPaths, msg.paths...)
		for _, p := range msg.paths {
			if !m.indexSet[p] {
				m.indexSet[p] = true
				m.indexPaths = append(m.indexPaths, p)
			}
		}
		if s, ok := m.modal.(*searchModal); ok {
			s.refilter(m)
		}
		return waitIndex(m.indexCh)
	case indexDoneMsg:
		if msg.gen != m.crawlGen {
			return nil
		}
		m.indexing = false
		if len(m.crawlPaths) > 0 {
			m.indexPaths = m.crawlPaths // drops paths that no longer exist
		}
		m.indexSet = nil
		m.indexUpdated = time.Now()
		if m.CacheDir != "" {
			ix := &index.Index{Addr: m.Addr, Updated: m.indexUpdated, Paths: m.indexPaths}
			if err := ix.Save(index.CachePath(m.CacheDir, m.Addr)); err != nil {
				m.flash("saving index: "+err.Error(), true)
			}
		}
	}
	return nil
}

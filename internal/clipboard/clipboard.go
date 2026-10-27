// Package clipboard copies values and clears them later, but only if the
// clipboard still holds what we put there.
package clipboard

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/atotto/clipboard"
)

type Manager struct {
	mu    sync.Mutex
	owned [32]byte // hash of what we wrote, so we don't keep the value around
	has   bool
	osc52 bool

	// Swappable for tests.
	read  func() (string, error)
	write func(string) error
	out   io.Writer // OSC 52 target
}

func New() *Manager {
	return &Manager{read: clipboard.ReadAll, write: clipboard.WriteAll, out: os.Stdout}
}

// Copy puts v on the clipboard. If there is no system clipboard it falls
// back to OSC 52 and returns osc52 == true.
func (m *Manager) Copy(v string) (osc52 bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !clipboard.Unsupported {
		if err = m.write(v); err == nil {
			m.owned, m.has, m.osc52 = sha256.Sum256([]byte(v)), true, false
			return false, nil
		}
	}
	// TODO(plan): inside tmux this needs set-clipboard on (or a DCS passthrough wrapper).
	if _, err := fmt.Fprintf(m.out, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(v))); err != nil {
		return true, err
	}
	m.owned, m.has, m.osc52 = sha256.Sum256([]byte(v)), true, true
	return true, nil
}

// ClearIfOwned empties the clipboard if it still holds our last value.
func (m *Manager) ClearIfOwned() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.has {
		return false
	}
	m.has = false
	if m.osc52 {
		// OSC 52 can't be read back, so we can't check ownership; clearing is
		// the safer default for secrets.
		fmt.Fprint(m.out, "\x1b]52;c;\a")
		return true
	}
	cur, err := m.read()
	if err != nil || sha256.Sum256([]byte(cur)) != m.owned {
		return false
	}
	return m.write("") == nil
}

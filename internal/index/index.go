// Package index crawls KV mounts for secret paths (never values), caches them
// on disk and fuzzy-searches them.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sahilm/fuzzy"

	"vaultui/internal/vault"
)

type Index struct {
	Addr    string    `json:"addr"`
	Updated time.Time `json:"updated"`
	Paths   []string  `json:"paths"`
}

func CachePath(dir, addr string) string {
	h := sha256.Sum256([]byte(addr))
	return filepath.Join(dir, "index-"+hex.EncodeToString(h[:8])+".json")
}

func Load(path string) (*Index, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ix Index
	return &ix, json.Unmarshal(b, &ix)
}

func (ix *Index) Save(path string) error {
	b, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (ix *Index) Stale(ttl time.Duration) bool { return time.Since(ix.Updated) > ttl }

type Match struct {
	Path    string
	Indexes []int // matched byte offsets, for highlighting
}

// Search returns the best `limit` fuzzy matches of query in paths.
func Search(query string, paths []string, limit int) []Match {
	ms := fuzzy.Find(query, paths)
	if len(ms) > limit {
		ms = ms[:limit]
	}
	out := make([]Match, len(ms))
	for i, m := range ms {
		out[i] = Match{m.Str, m.MatchedIndexes}
	}
	return out
}

type Options struct {
	Concurrency int
	MaxDepth    int
	MaxPaths    int
}

// Crawl lists every mount recursively with at most o.Concurrency requests in
// flight, calling found (serialised) with batches of secret paths like
// "secret/a/b". Errors, including 403s, are skipped silently.
func Crawl(ctx context.Context, store vault.KVStore, mounts []vault.Mount, o Options, found func([]string)) {
	sem := make(chan struct{}, max(o.Concurrency, 1))
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		count atomic.Int64
	)
	var walk func(mount, path string, depth int)
	walk = func(mount, path string, depth int) {
		defer wg.Done()
		if depth > o.MaxDepth || count.Load() >= int64(o.MaxPaths) {
			return
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		es, err := store.List(ctx, mount, path)
		<-sem
		if err != nil {
			return
		}
		var leaves []string
		for _, e := range es {
			if e.IsDir {
				wg.Add(1)
				go walk(mount, path+e.Name, depth+1)
			} else if count.Add(1) <= int64(o.MaxPaths) {
				leaves = append(leaves, mount+path+e.Name)
			}
		}
		if len(leaves) > 0 {
			mu.Lock()
			found(leaves)
			mu.Unlock()
		}
	}
	for _, m := range mounts {
		wg.Add(1)
		go walk(m.Path, "", 0)
	}
	wg.Wait()
}

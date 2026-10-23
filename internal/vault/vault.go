// Package vault hides KV v1/v2 differences behind KVStore. Read-only: it only
// issues GET and LIST requests.
package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/vault/api"
)

var (
	ErrForbidden = errors.New("permission denied")
	ErrNotFound  = errors.New("not found")
)

type Mount struct {
	Path    string // with trailing slash, e.g. "secret/"
	Version int    // 1 or 2
}

type Entry struct {
	Name  string // folders end in "/"
	IsDir bool
}

type Secret struct {
	Mount, Path string
	KVVersion   int
	Data        map[string]string
	Version     int // 0 for KV v1
	Created     time.Time
	Deleted     bool
	Destroyed   bool
}

// FullPath is mount + path, e.g. "secret/payments/prod/db".
func (s *Secret) FullPath() string { return s.Mount + s.Path }

type VersionMeta struct {
	Version   int
	Created   time.Time
	Deleted   bool
	Destroyed bool
}

type KVStore interface {
	Mounts(ctx context.Context) ([]Mount, error)
	List(ctx context.Context, mount, path string) ([]Entry, error)
	Read(ctx context.Context, mount, path string, version int) (*Secret, error) // version 0 = latest
	Versions(ctx context.Context, mount, path string) ([]VersionMeta, error)    // empty for v1
}

// NewClient builds an API client from the environment (VAULT_ADDR,
// VAULT_NAMESPACE, VAULT_CACERT, VAULT_SKIP_VERIFY...). A non-empty addr or
// namespace (a saved Vault) overrides the environment. If debug is non-nil,
// request method/path/status are logged to it (never bodies, query strings or
// headers).
func NewClient(addr, namespace string, debug io.Writer) (*api.Client, error) {
	conf := api.DefaultConfig()
	if conf.Error != nil {
		return nil, conf.Error
	}
	if addr != "" {
		conf.Address = addr
	}
	c, err := api.NewClient(conf)
	if err != nil {
		return nil, err
	}
	if namespace != "" {
		c.SetNamespace(namespace)
	}
	if debug != nil {
		conf.HttpClient.Transport = &logTransport{conf.HttpClient.Transport, debug}
	}
	return c, nil
}

type logTransport struct {
	next http.RoundTripper
	w    io.Writer
}

func (t *logTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.next.RoundTrip(r)
	status := "ERR"
	if resp != nil {
		status = strconv.Itoa(resp.StatusCode)
	}
	fmt.Fprintf(t.w, "%s %s %s %s %v\n", start.Format(time.RFC3339), r.Method, r.URL.Path, status, time.Since(start).Round(time.Millisecond))
	return resp, err
}

type Store struct {
	c        *api.Client
	fallback []string

	mu       sync.Mutex
	versions map[string]int
	acl      []aclPath // nil until fetched
}

type aclPath struct {
	path string
	glob bool
}

func NewStore(c *api.Client, fallbackMounts []string) *Store {
	return &Store{c: c, fallback: fallbackMounts, versions: map[string]int{}}
}

func wrap(err error) error {
	var re *api.ResponseError
	if errors.As(err, &re) {
		switch re.StatusCode {
		case http.StatusForbidden:
			return ErrForbidden
		case http.StatusNotFound:
			return ErrNotFound
		}
	}
	return err
}

func normMount(m string) string { return strings.Trim(m, "/") + "/" }

func (s *Store) Mounts(ctx context.Context) ([]Mount, error) {
	var out []Mount
	if sec, err := s.c.Logical().ReadWithContext(ctx, "sys/internal/ui/mounts"); err == nil && sec != nil {
		secretMounts, _ := sec.Data["secret"].(map[string]any)
		for p, v := range secretMounts {
			m, _ := v.(map[string]any)
			if m["type"] != "kv" {
				continue
			}
			out = append(out, Mount{Path: normMount(p), Version: optVersion(m)})
		}
	}
	if len(out) == 0 {
		for _, p := range s.fallback {
			p = normMount(p)
			out = append(out, Mount{Path: p, Version: s.preflight(ctx, p)})
		}
	}
	if len(out) == 0 {
		return nil, errors.New("no KV mounts found; list them under `mounts:` in the config file")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	s.mu.Lock()
	for _, m := range out {
		s.versions[m.Path] = m.Version
	}
	s.mu.Unlock()
	return out, nil
}

func optVersion(m map[string]any) int {
	opts, _ := m["options"].(map[string]any)
	if opts["version"] == "2" {
		return 2
	}
	return 1
}

// preflight asks Vault for one mount's KV version, like `vault kv` does.
func (s *Store) preflight(ctx context.Context, mount string) int {
	sec, err := s.c.Logical().ReadWithContext(ctx, "sys/internal/ui/mounts/"+mount)
	if err != nil || sec == nil {
		return 2 // TODO(plan): unknown version; v2 is the common default
	}
	return optVersion(sec.Data)
}

func (s *Store) version(ctx context.Context, mount string) int {
	s.mu.Lock()
	v, ok := s.versions[mount]
	s.mu.Unlock()
	if !ok {
		v = s.preflight(ctx, mount)
		s.mu.Lock()
		s.versions[mount] = v
		s.mu.Unlock()
	}
	return v
}

func (s *Store) List(ctx context.Context, mount, path string) ([]Entry, error) {
	mount = normMount(mount)
	p := mount + path
	if s.version(ctx, mount) == 2 {
		p = mount + "metadata/" + path
	}
	sec, err := s.c.Logical().ListWithContext(ctx, p)
	if err = wrap(err); errors.Is(err, ErrForbidden) {
		if es := s.aclEntries(ctx, mount, path); len(es) > 0 {
			return sortEntries(es), nil
		}
	}
	if err != nil {
		return nil, err
	}
	if sec == nil {
		return nil, nil
	}
	keys, _ := sec.Data["keys"].([]any)
	out := make([]Entry, 0, len(keys))
	for _, k := range keys {
		name, _ := k.(string)
		out = append(out, Entry{Name: name, IsDir: strings.HasSuffix(name, "/")})
	}
	return sortEntries(out), nil
}

func sortEntries(out []Entry) []Entry {
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// aclEntries derives the children of a folder we may not LIST from the
// token's own policies (sys/internal/ui/resultant-acl), like the web UI does.
// It only shows paths the policy grants; it never retries the denied request.
func (s *Store) aclEntries(ctx context.Context, mount, path string) []Entry {
	s.mu.Lock()
	acl := s.acl
	s.mu.Unlock()
	if acl == nil {
		acl = []aclPath{}
		if sec, err := s.c.Logical().ReadWithContext(ctx, "sys/internal/ui/resultant-acl"); err == nil && sec != nil {
			for kind, glob := range map[string]bool{"exact_paths": false, "glob_paths": true} {
				ps, _ := sec.Data[kind].(map[string]any)
				for p, v := range ps {
					caps, _ := v.(map[string]any)["capabilities"].([]any)
					if hasCap(caps, "read") || hasCap(caps, "list") {
						acl = append(acl, aclPath{strings.TrimSuffix(p, "*"), glob})
					}
				}
			}
		}
		s.mu.Lock()
		s.acl = acl
		s.mu.Unlock()
	}

	v2 := s.version(ctx, mount) == 2
	seen := map[string]bool{}
	var out []Entry
	for _, a := range acl {
		rel, ok := strings.CutPrefix(a.path, mount)
		if !ok {
			continue
		}
		if v2 {
			if r, ok := strings.CutPrefix(rel, "data/"); ok {
				rel = r
			} else if r, ok := strings.CutPrefix(rel, "metadata/"); ok {
				rel = r
			} else {
				continue
			}
		}
		rest, ok := strings.CutPrefix(rel, path)
		if !ok || rest == "" {
			continue
		}
		e := Entry{Name: rest}
		if i := strings.Index(rest, "/"); i >= 0 {
			e = Entry{Name: rest[:i+1], IsDir: true}
		} else if a.glob {
			continue // "team-*" style prefix: no concrete name to show
		}
		if strings.ContainsAny(e.Name, "+*") || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	return out
}

func hasCap(caps []any, c string) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

func (s *Store) Read(ctx context.Context, mount, path string, version int) (*Secret, error) {
	mount = normMount(mount)
	kv := s.version(ctx, mount)
	out := &Secret{Mount: mount, Path: path, KVVersion: kv, Data: map[string]string{}}
	if kv == 1 {
		sec, err := s.c.Logical().ReadWithContext(ctx, mount+path)
		if err != nil {
			return nil, wrap(err)
		}
		if sec == nil {
			return nil, ErrNotFound
		}
		fillData(out.Data, sec.Data)
		return out, nil
	}
	var q map[string][]string
	if version > 0 {
		q = map[string][]string{"version": {strconv.Itoa(version)}}
	}
	sec, err := s.c.Logical().ReadWithDataWithContext(ctx, mount+"data/"+path, q)
	if err != nil {
		return nil, wrap(err)
	}
	if sec == nil {
		return nil, ErrNotFound
	}
	data, _ := sec.Data["data"].(map[string]any)
	fillData(out.Data, data)
	md, _ := sec.Data["metadata"].(map[string]any)
	out.Version = jsonInt(md["version"])
	out.Created = parseTime(md["created_time"])
	out.Deleted = parseTime(md["deletion_time"]).After(time.Time{})
	out.Destroyed, _ = md["destroyed"].(bool)
	return out, nil
}

func (s *Store) Versions(ctx context.Context, mount, path string) ([]VersionMeta, error) {
	mount = normMount(mount)
	if s.version(ctx, mount) == 1 {
		return nil, nil
	}
	sec, err := s.c.Logical().ReadWithContext(ctx, mount+"metadata/"+path)
	if err != nil {
		return nil, wrap(err)
	}
	if sec == nil {
		return nil, ErrNotFound
	}
	vs, _ := sec.Data["versions"].(map[string]any)
	out := make([]VersionMeta, 0, len(vs))
	for k, v := range vs {
		m, _ := v.(map[string]any)
		n, _ := strconv.Atoi(k)
		destroyed, _ := m["destroyed"].(bool)
		out = append(out, VersionMeta{
			Version:   n,
			Created:   parseTime(m["created_time"]),
			Deleted:   parseTime(m["deletion_time"]).After(time.Time{}),
			Destroyed: destroyed,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}

func fillData(dst map[string]string, src map[string]any) {
	for k, v := range src {
		if s, ok := v.(string); ok {
			dst[k] = s
			continue
		}
		b, _ := json.Marshal(v)
		dst[k] = string(b)
	}
}

func jsonInt(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case float64:
		return int(n)
	}
	return 0
}

func parseTime(v any) time.Time {
	s, _ := v.(string)
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// SplitPath splits "secret/a/b" into ("secret/", "a/b") using the longest
// matching mount.
func SplitPath(full string, mounts []Mount) (mount, path string, ok bool) {
	for _, m := range mounts {
		if strings.HasPrefix(full, m.Path) && len(m.Path) > len(mount) {
			mount = m.Path
		}
	}
	if mount == "" {
		return "", "", false
	}
	return mount, strings.TrimPrefix(full, mount), true
}

// Package config loads and saves ~/.config/vaultui/config.yaml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Auth struct {
	Method     string `yaml:"method"`
	Mount      string `yaml:"mount"`
	Role       string `yaml:"role"`
	ListenPort int    `yaml:"listen_port"`
	SaveToken  bool   `yaml:"save_token"`
}

type UI struct {
	RevealTimeoutSeconds  int    `yaml:"reveal_timeout_seconds"`
	ClipboardClearSeconds int    `yaml:"clipboard_clear_seconds"`
	Theme                 string `yaml:"theme"`
}

type Search struct {
	Concurrency int           `yaml:"concurrency"`
	MaxDepth    int           `yaml:"max_depth"`
	MaxPaths    int           `yaml:"max_paths"`
	IndexTTL    time.Duration `yaml:"index_ttl"`
	IndexKeys   bool          `yaml:"index_keys"`
}

type Favorite struct {
	Alias string `yaml:"alias"`
	Path  string `yaml:"path"`
}

// Vault is one saved Vault server. Favorites and recent paths are per Vault.
type Vault struct {
	Name      string     `yaml:"name"`
	Addr      string     `yaml:"addr"`
	Namespace string     `yaml:"namespace,omitempty"`
	AuthMount string     `yaml:"auth_mount,omitempty"` // overrides auth.mount
	AuthRole  string     `yaml:"auth_role,omitempty"`  // overrides auth.role
	Favorites []Favorite `yaml:"favorites,omitempty"`
	Recent    []string   `yaml:"recent,omitempty"`
}

type Config struct {
	// Deprecated: migrated into Vaults on load.
	VaultAddr string `yaml:"vault_addr,omitempty"`
	Namespace string `yaml:"namespace,omitempty"`

	Vaults    []Vault `yaml:"vaults"`
	LastVault string  `yaml:"last_vault,omitempty"`

	Auth         Auth     `yaml:"auth"`
	Mounts       []string `yaml:"mounts"`
	UI           UI       `yaml:"ui"`
	Search       Search   `yaml:"search"`
	Environments []string `yaml:"environments"`
	// Favorites and Recent are the working lists for the Vault in use (see
	// Use). They're only written at the top level when no saved Vault is
	// active, e.g. when VAULT_ADDR points somewhere unsaved.
	Favorites []Favorite `yaml:"favorites,omitempty"`
	Recent    []string   `yaml:"recent,omitempty"`

	path   string
	active int // index into Vaults, -1 if none
}

const maxRecent = 20

func Default() *Config {
	return &Config{
		Auth:         Auth{Method: "oidc", Mount: "oidc", ListenPort: 8250, SaveToken: true},
		Mounts:       []string{},
		UI:           UI{RevealTimeoutSeconds: 30, ClipboardClearSeconds: 20, Theme: "auto"},
		Search:       Search{Concurrency: 8, MaxDepth: 20, MaxPaths: 50000, IndexTTL: time.Hour},
		Environments: []string{"dev", "staging", "qa", "prod"},
		Vaults:       []Vault{},
		active:       -1,
	}
}

// DefaultPath honours XDG_CONFIG_HOME, else ~/.config.
func DefaultPath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "vaultui", "config.yaml"), nil
}

// Load reads the config at path, creating it with defaults if missing.
// Missing keys keep their default values.
func Load(path string) (*Config, error) {
	c := Default()
	c.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, c.Save()
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, err
	}
	if len(c.Vaults) == 0 && c.VaultAddr != "" { // old single-Vault config
		addr, _ := NormalizeAddr(c.VaultAddr)
		c.Vaults = []Vault{{Name: hostOf(addr), Addr: addr, Namespace: c.Namespace, Favorites: c.Favorites, Recent: c.Recent}}
		c.LastVault = c.Vaults[0].Name
		c.VaultAddr, c.Namespace, c.Favorites, c.Recent = "", "", nil, nil
		return c, c.Save()
	}
	return c, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	out := *c
	if v := c.Active(); v != nil {
		v.Favorites, v.Recent = c.Favorites, c.Recent
		out.Vaults = append([]Vault(nil), c.Vaults...)
		out.Favorites, out.Recent = nil, nil
	}
	b, err := yaml.Marshal(&out)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, b, 0o600)
}

// FavoriteIndex returns the index of path in Favorites, or -1.
func (c *Config) FavoriteIndex(path string) int {
	for i, f := range c.Favorites {
		if f.Path == path {
			return i
		}
	}
	return -1
}

// ToggleFavorite adds or removes path; returns true if it is now a favorite.
func (c *Config) ToggleFavorite(path string) bool {
	if i := c.FavoriteIndex(path); i >= 0 {
		c.Favorites = append(c.Favorites[:i], c.Favorites[i+1:]...)
		return false
	}
	c.Favorites = append(c.Favorites, Favorite{Path: path})
	return true
}

// AddRecent moves path to the front of Recent, capped at 20.
func (c *Config) AddRecent(path string) {
	out := []string{path}
	for _, p := range c.Recent {
		if p != path && len(out) < maxRecent {
			out = append(out, p)
		}
	}
	c.Recent = out
}

// Active is the saved Vault in use, or nil.
func (c *Config) Active() *Vault {
	if c.active < 0 || c.active >= len(c.Vaults) {
		return nil
	}
	return &c.Vaults[c.active]
}

// Use makes the named Vault active and loads its favorites and recent paths.
func (c *Config) Use(name string) error {
	if v := c.Active(); v != nil { // keep the outgoing Vault's lists
		v.Favorites, v.Recent = c.Favorites, c.Recent
	}
	for i, v := range c.Vaults {
		if v.Name == name {
			c.active, c.LastVault = i, name
			c.Favorites, c.Recent = v.Favorites, v.Recent
			return nil
		}
	}
	return fmt.Errorf("no saved Vault named %q (saved: %s)", name, strings.Join(c.VaultNames(), ", "))
}

// UseAddr activates the saved Vault with this address, if there is one.
func (c *Config) UseAddr(addr string) bool {
	n, _ := NormalizeAddr(addr)
	for _, v := range c.Vaults {
		if v.Addr == n {
			return c.Use(v.Name) == nil
		}
	}
	return false
}

func (c *Config) VaultNames() []string {
	var ns []string
	for _, v := range c.Vaults {
		ns = append(ns, v.Name)
	}
	return ns
}

// EffectiveAuth is the auth config with the active Vault's overrides applied.
func (c *Config) EffectiveAuth() Auth {
	a := c.Auth
	if v := c.Active(); v != nil {
		if v.AuthMount != "" {
			a.Mount = v.AuthMount
		}
		if v.AuthRole != "" {
			a.Role = v.AuthRole
		}
	}
	return a
}

// PutVault validates v and adds it (idx < 0) or replaces Vaults[idx].
func (c *Config) PutVault(idx int, v Vault) error {
	v.Name = strings.TrimSpace(v.Name)
	addr, err := NormalizeAddr(v.Addr)
	if err != nil {
		return err
	}
	v.Addr = addr
	if v.Name == "" {
		v.Name = hostOf(addr)
	}
	for i, o := range c.Vaults {
		if i != idx && o.Name == v.Name {
			return fmt.Errorf("a Vault named %q already exists", v.Name)
		}
	}
	if idx < 0 {
		c.Vaults = append(c.Vaults, v)
		return nil
	}
	old := c.Vaults[idx]
	v.Favorites, v.Recent = old.Favorites, old.Recent
	if c.LastVault == old.Name {
		c.LastVault = v.Name
	}
	c.Vaults[idx] = v
	return nil
}

func (c *Config) DeleteVault(idx int) {
	if c.Vaults[idx].Name == c.LastVault {
		c.LastVault = ""
	}
	c.Vaults = append(c.Vaults[:idx], c.Vaults[idx+1:]...)
	if c.active == idx {
		c.active = -1
	} else if c.active > idx {
		c.active--
	}
}

// NormalizeAddr accepts what people paste (including web UI links like
// https://vault.x.com/ui/vault/auth?with=oidc) and returns scheme://host[/path].
func NormalizeAddr(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw != "" && !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("enter a Vault URL like https://vault.company.com")
	}
	p := u.Path
	if i := strings.Index(p, "/ui"); i >= 0 && (len(p) == i+3 || p[i+3] == '/') {
		p = p[:i]
	}
	return u.Scheme + "://" + u.Host + strings.TrimSuffix(p, "/"), nil
}

func hostOf(addr string) string {
	if u, err := url.Parse(addr); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return addr
}

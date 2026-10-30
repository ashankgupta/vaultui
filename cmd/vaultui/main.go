package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/hashicorp/vault/api"

	"vaultui/internal/auth"
	"vaultui/internal/config"
	"vaultui/internal/export"
	"vaultui/internal/ui"
	"vaultui/internal/vault"
)

const usage = `vaultui — a read-only terminal UI for HashiCorp Vault KV
Created by Ashank Gupta — https://github.com/ashankgupta

Usage:
  vaultui [flags]                    pick a saved Vault (or add one) and start the TUI
  vaultui [flags] login              log in and print the token's identity and TTL
  vaultui [flags] export <path> [--format F] [--upper] [--prefix P]
                                     print a secret to stdout; F is dotenv (default),
                                     shell, k8s, docker or json

Which Vault: --vault NAME, else VAULT_ADDR, else your only saved Vault, else
the picker (export uses the last one you used instead of asking).

Flags:
`

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

type opts struct {
	tokenOnly, debug, pick bool
	vault                  string
}

func main() {
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	var o opts
	flag.BoolVar(&o.tokenOnly, "token-only", false, "never start OIDC; fail if there's no valid token")
	flag.BoolVar(&o.debug, "debug", false, "log Vault requests (paths only) to ~/.cache/vaultui/debug.log")
	flag.StringVar(&o.vault, "vault", "", "name of a saved Vault to use")
	flag.BoolVar(&o.pick, "pick", false, "always show the Vault picker")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("vaultui %s — Created by Ashank Gupta (https://github.com/ashankgupta)\n", version)
		return
	}

	if err := run(flag.Args(), o); err != nil {
		fmt.Fprintln(os.Stderr, "vaultui:", err)
		os.Exit(1)
	}
}

type env struct {
	cfg    *config.Config
	client *api.Client
	info   *auth.Info
	cache  string
}

var errQuit = errors.New("quit")

// chooseVault activates the Vault to use and returns its address and
// namespace ("" means: take them from the environment). interactive allows the
// picker; pickErr is shown in it (e.g. why the last connection failed).
func chooseVault(cfg *config.Config, o opts, interactive bool, pickErr string) (addr, ns string, err error) {
	envAddr := os.Getenv("VAULT_ADDR")
	switch {
	case o.vault != "":
		if err := cfg.Use(o.vault); err != nil {
			return "", "", err
		}
	case envAddr != "" && !o.pick:
		if !cfg.UseAddr(envAddr) {
			return "", "", nil // unsaved Vault from the environment
		}
	case !interactive:
		name := cfg.LastVault
		if name == "" && len(cfg.Vaults) == 1 {
			name = cfg.Vaults[0].Name
		}
		if name == "" {
			if len(cfg.Vaults) == 0 {
				return "", "", errors.New("no Vault configured: run `vaultui` to add one, or set VAULT_ADDR")
			}
			return "", "", fmt.Errorf("which Vault? pass --vault NAME (saved: %s)", strings.Join(cfg.VaultNames(), ", "))
		}
		if err := cfg.Use(name); err != nil {
			return "", "", err
		}
	case len(cfg.Vaults) == 1 && !o.pick && pickErr == "":
		cfg.Use(cfg.Vaults[0].Name)
	default:
		p := ui.NewPicker(cfg)
		p.Err = pickErr
		final, err := tea.NewProgram(p, tea.WithAltScreen()).Run()
		if err != nil {
			return "", "", err
		}
		chosen := final.(*ui.Picker).Chosen
		if chosen == "" {
			return "", "", errQuit
		}
		cfg.Use(chosen)
	}
	if err := cfg.Save(); err != nil { // remembers the last used Vault
		return "", "", err
	}
	v := cfg.Active()
	return v.Addr, v.Namespace, nil
}

func connect(cfg *config.Config, addr, ns string, o opts, dbg io.Writer) (*env, error) {
	c, err := vault.NewClient(addr, ns, dbg)
	if err != nil {
		return nil, err
	}
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	name := c.Address()
	if v := cfg.Active(); v != nil {
		name = v.Name + " (" + v.Addr + ")"
	}
	fmt.Fprintf(os.Stderr, "Connecting to %s…\n", name)
	info, err := auth.Resolve(context.Background(), c, cfg.EffectiveAuth(), o.tokenOnly, func(u string) {
		fmt.Fprintf(os.Stderr, "Complete the login in your browser. If it didn't open, visit:\n\n  %s\n\n", u)
	}, auth.VaultTokenFile(dir, c.Address()))
	if err != nil {
		return nil, err
	}
	return &env{cfg, c, info, dir}, nil
}

func loadConfig() (*config.Config, error) {
	p, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(p)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", p, err)
	}
	return cfg, nil
}

func debugWriter(on bool) (io.Writer, error) {
	if !on {
		return nil, nil
	}
	dir, err := cacheDir()
	if err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dir, "debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func cacheDir() (string, error) {
	dir := os.Getenv("XDG_CACHE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".cache")
	}
	dir = filepath.Join(dir, "vaultui")
	return dir, os.MkdirAll(dir, 0o700)
}

func run(args []string, o opts) error {
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	dbg, err := debugWriter(o.debug)
	if err != nil {
		return err
	}
	switch cmd {
	case "":
		pickErr := ""
		for {
			addr, ns, err := chooseVault(cfg, o, true, pickErr)
			if errors.Is(err, errQuit) {
				return nil
			} else if err != nil {
				return err
			}
			e, err := connect(cfg, addr, ns, o, dbg)
			if err != nil {
				if o.vault != "" || (os.Getenv("VAULT_ADDR") != "" && !o.pick) {
					return err // the Vault came from a flag/env; nothing to pick instead
				}
				pickErr, o.pick = err.Error(), true // back to the picker, showing why
				continue
			}
			pickErr = ""
			m := ui.New(ui.Options{
				Store: vault.NewStore(e.client, cfg.Mounts), Config: cfg, Info: e.info,
				Addr: e.client.Address(), CacheDir: e.cache, VaultName: activeName(cfg),
				Relogin: func(ctx context.Context) (*auth.Info, error) {
					return auth.LoginOIDC(ctx, e.client, cfg.EffectiveAuth(), nil, auth.VaultTokenFile(e.cache, e.client.Address()))
				},
			})
			final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
			if err != nil {
				return err
			}
			if !final.(*ui.Model).SwitchVault {
				return nil
			}
			o.pick, o.vault = true, ""
		}
	case "login":
		addr, ns, err := chooseVault(cfg, o, true, "")
		if errors.Is(err, errQuit) {
			return nil
		} else if err != nil {
			return err
		}
		e, err := connect(cfg, addr, ns, o, dbg)
		if err != nil {
			return err
		}
		ttl := "never expires"
		if !e.info.Expire.IsZero() {
			ttl = e.info.TTL().String()
		}
		fmt.Printf("Logged in to %s as %s (%s), TTL %s\n", e.client.Address(), e.info.DisplayName, e.info.Method, ttl)
		return nil
	case "export":
		return runExport(cfg, args, o, dbg)
	default:
		flag.Usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func activeName(cfg *config.Config) string {
	if v := cfg.Active(); v != nil {
		return v.Name
	}
	return ""
}

func runExport(cfg *config.Config, args []string, o opts, dbg io.Writer) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	upper := fs.Bool("upper", false, "convert keys to UPPER_SNAKE_CASE")
	prefix := fs.String("prefix", "", "prefix for every key, e.g. PAYMENTS_")
	format := fs.String("format", "dotenv", "dotenv, shell, k8s, docker or json")
	// Allow flags before or after the path.
	fs.Parse(args)
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: vaultui export <mount/path> [--format dotenv|shell|k8s|docker|json] [--upper] [--prefix P]")
	}
	full := fs.Arg(0)
	fs.Parse(fs.Args()[1:])
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	addr, ns, err := chooseVault(cfg, o, false, "")
	if err != nil {
		return err
	}
	e, err := connect(cfg, addr, ns, o, dbg)
	if err != nil {
		return err
	}
	store := vault.NewStore(e.client, cfg.Mounts)
	ctx := context.Background()
	mounts, err := store.Mounts(ctx)
	if err != nil {
		return err
	}
	mount, path, ok := vault.SplitPath(full, mounts)
	if !ok {
		return fmt.Errorf("no KV mount for %s", full)
	}
	s, err := store.Read(ctx, mount, path, 0)
	if err != nil {
		return fmt.Errorf("%s: %w", full, err)
	}
	out, err := export.Render(*format, path[strings.LastIndex(path, "/")+1:], s.Data, export.Options{Upper: *upper, Prefix: *prefix})
	if err != nil {
		return err
	}
	_, err = fmt.Print(out)
	return err
}

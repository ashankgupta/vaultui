// Package auth resolves a Vault token: VAULT_TOKEN → ~/.vault-token → OIDC.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
	"github.com/pkg/browser"

	"vaultui/internal/config"
)

// Info describes the token in use. Expire is zero for non-expiring tokens.
type Info struct {
	DisplayName string
	Method      string
	Expire      time.Time
}

// TTL is the remaining lifetime; 0 for non-expiring tokens.
func (i *Info) TTL() time.Duration {
	if i.Expire.IsZero() {
		return 0
	}
	return time.Until(i.Expire).Round(time.Second)
}

var ErrNoToken = errors.New("no valid Vault token (set VAULT_TOKEN or run `vaultui login`)")

// TokenFile is ~/.vault-token, shared with the vault CLI.
func TokenFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vault-token")
}

// VaultTokenFile is where the token for one Vault address is kept, so
// switching between saved Vaults doesn't need a new login each time.
func VaultTokenFile(dir, addr string) string {
	h := sha256.Sum256([]byte(addr))
	return filepath.Join(dir, "tokens", hex.EncodeToString(h[:8]))
}

// Resolve sets a valid token on c, trying VAULT_TOKEN, then vaultTokenFile
// (per Vault; may be ""), then ~/.vault-token, then OIDC. onURL receives the
// OIDC auth URL (fallback for when the browser doesn't open).
func Resolve(ctx context.Context, c *api.Client, a config.Auth, tokenOnly bool, onURL func(string), vaultTokenFile string) (*Info, error) {
	read := func(p string) string {
		if p == "" {
			return ""
		}
		b, _ := os.ReadFile(p)
		return strings.TrimSpace(string(b))
	}
	toks := []string{read(vaultTokenFile)}
	// VAULT_TOKEN and ~/.vault-token belong to VAULT_ADDR's server; don't
	// send them to a different Vault picked in the UI.
	if env := os.Getenv("VAULT_ADDR"); env == "" || strings.TrimSuffix(env, "/") == c.Address() {
		toks = []string{os.Getenv("VAULT_TOKEN"), toks[0], read(TokenFile())}
	}
	for _, tok := range toks {
		if tok == "" {
			continue
		}
		c.SetToken(tok)
		if info, err := LookupSelf(ctx, c); err == nil {
			return info, nil
		}
	}
	c.ClearToken()
	if tokenOnly {
		return nil, ErrNoToken
	}
	return LoginOIDC(ctx, c, a, onURL, vaultTokenFile)
}

// LookupSelf validates the client's token.
func LookupSelf(ctx context.Context, c *api.Client) (*Info, error) {
	s, err := c.Auth().Token().LookupSelfWithContext(ctx)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrNoToken
	}
	info := &Info{Method: "token"}
	info.DisplayName, _ = s.Data["display_name"].(string)
	if p, _ := s.Data["path"].(string); strings.HasPrefix(p, "auth/") {
		info.Method = strings.SplitN(p, "/", 3)[1]
	}
	if ttl, err := s.TokenTTL(); err == nil && ttl > 0 {
		info.Expire = time.Now().Add(ttl)
	}
	return info, nil
}

// LoginOIDC runs the browser flow, same as `vault login -method=oidc`.
func LoginOIDC(ctx context.Context, c *api.Client, a config.Auth, onURL func(string), vaultTokenFile string) (*Info, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", a.ListenPort))
	if err != nil {
		return nil, fmt.Errorf("start OIDC callback listener: %w", err)
	}
	redirect := fmt.Sprintf("http://localhost:%d/oidc/callback", a.ListenPort)
	nb := make([]byte, 16)
	rand.Read(nb)
	nonce := hex.EncodeToString(nb)
	base := "auth/" + strings.Trim(a.Mount, "/") + "/oidc/"
	// Login calls go out without the (possibly expired) current token.
	ac, err := c.Clone()
	if err != nil {
		ln.Close()
		return nil, err
	}
	ac.ClearToken()
	ac.SetNamespace(c.Namespace()) // the namespace header isn't cloned

	s, err := ac.Logical().WriteWithContext(ctx, base+"auth_url", map[string]any{
		"role": a.Role, "redirect_uri": redirect, "client_nonce": nonce,
	})
	authURL := ""
	if s != nil {
		authURL, _ = s.Data["auth_url"].(string)
	}
	if err != nil || authURL == "" {
		ln.Close()
		if err == nil || strings.Contains(err.Error(), "redirect") {
			return nil, fmt.Errorf("OIDC login rejected the redirect URI (%v).\nAsk the Vault admins to add %s to the role's allowed_redirect_uris", err, redirect)
		}
		return nil, fmt.Errorf("OIDC login via auth/%s failed: %w", strings.Trim(a.Mount, "/"), err)
	}

	type result struct {
		tok string
		err error
	}
	done := make(chan result, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oidc/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var res result
		if e := q.Get("error"); e != "" {
			res.err = fmt.Errorf("OIDC provider error: %s %s", e, q.Get("error_description"))
		} else {
			s, err := ac.Logical().ReadWithDataWithContext(r.Context(), base+"callback", map[string][]string{
				"state": {q.Get("state")}, "code": {q.Get("code")}, "client_nonce": {nonce},
			})
			switch {
			case err != nil:
				res.err = err
			case s == nil || s.Auth == nil || s.Auth.ClientToken == "":
				res.err = errors.New("OIDC callback returned no token")
			default:
				res.tok = s.Auth.ClientToken
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprint(w, "<h2>vaultui: login failed</h2><p>Check the terminal for details.</p>")
		} else {
			fmt.Fprint(w, "<h2>vaultui: logged in ✔</h2><p>You can close this tab.</p>")
		}
		select {
		case done <- res:
		default:
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	defer srv.Shutdown(context.Background())

	if onURL != nil {
		onURL(authURL)
	}
	browser.Stdout, browser.Stderr = io.Discard, io.Discard
	_ = browser.OpenURL(authURL)

	var res result
	select {
	case res = <-done:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, errors.New("timed out waiting for OIDC login")
	}
	if res.err != nil {
		return nil, res.err
	}
	c.SetToken(res.tok)
	if a.SaveToken {
		for _, p := range []string{TokenFile(), vaultTokenFile} {
			if p == "" {
				continue
			}
			if err := writeToken(p, res.tok); err != nil {
				return nil, fmt.Errorf("logged in, but saving %s failed: %w", p, err)
			}
		}
	}
	return LookupSelf(ctx, c)
}

// writeToken writes a token file with 0600 (its directory 0700).
func writeToken(p, tok string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(tok), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600) // WriteFile keeps the mode of an existing file
}

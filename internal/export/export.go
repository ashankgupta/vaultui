// Package export formats secrets as .env and writes them with 0600.
package export

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type Options struct {
	Upper  bool   // UPPER_SNAKE_CASE keys
	Prefix string // prepended to every key
}

// Format renders data as KEY="value" lines, sorted by key.
func Format(data map[string]string, o Options) string {
	var b strings.Builder
	for _, k := range sortedKeys(data) {
		b.WriteString(Key(k, o) + "=" + Quote(data[k]) + "\n")
	}
	return b.String()
}

func Key(k string, o Options) string {
	if o.Upper {
		k = UpperSnake(k)
	}
	return o.Prefix + k
}

// Quote double-quotes v, escaping \ " $ and newlines.
func Quote(v string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(v) + `"`
}

// UpperSnake: "dbPassword" → "DB_PASSWORD", "api-key.v2" → "API_KEY_V2",
// "HTTPServer" → "HTTP_SERVER".
func UpperSnake(s string) string {
	rs := []rune(s)
	var b strings.Builder
	under := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "_") {
			b.WriteByte('_')
		}
	}
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			under()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				under()
			}
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return strings.TrimSuffix(b.String(), "_")
}

// Write creates or appends to path with 0600 permissions.
func Write(path, content string, appendMode bool) error {
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if appendMode {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	f, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil { // existing files keep their old mode otherwise
		f.Close()
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// GitIgnored reports whether path's directory is inside a git work tree and,
// if so, whether path is ignored. No git → (false, false).
func GitIgnored(path string) (inRepo, ignored bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false, false
	}
	dir := filepath.Dir(abs)
	if exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run() != nil {
		return false, false
	}
	err = exec.Command("git", "-C", dir, "check-ignore", "-q", abs).Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return true, false // exit 1: not ignored (or tracked, which is just as bad)
	}
	return true, err == nil
}

// Output formats for Render.
var Formats = []struct{ ID, Label string }{
	{"shell", "Shell exports      export KEY='value'"},
	{"dotenv", ".env               KEY=\"value\""},
	{"k8s", "Kubernetes Secret  YAML manifest (stringData)"},
	{"docker", "Docker flags       -e 'KEY=value' …"},
	{"json", "JSON               {\"KEY\": \"value\"}"},
}

// Render formats data as one of Formats. name is used for the Kubernetes
// Secret's metadata.name (usually the secret's leaf path segment).
func Render(format, name string, data map[string]string, o Options) (string, error) {
	keys := sortedKeys(data)
	var b strings.Builder
	switch format {
	case "dotenv", "":
		return Format(data, o), nil
	case "shell":
		for _, k := range keys {
			fmt.Fprintf(&b, "export %s=%s\n", EnvName(Key(k, o)), ShellQuote(data[k]))
		}
	case "docker":
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = "-e " + ShellQuote(EnvName(Key(k, o))+"="+data[k])
		}
		b.WriteString(strings.Join(parts, " ") + "\n")
	case "k8s":
		fmt.Fprintf(&b, "apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\ntype: Opaque\nstringData:\n", K8sName(name))
		for _, k := range keys {
			kq, _ := json.Marshal(Key(k, o)) // JSON strings are valid YAML double-quoted scalars
			vq, _ := json.Marshal(data[k])
			fmt.Fprintf(&b, "  %s: %s\n", kq, vq)
		}
	case "json":
		out := map[string]string{}
		for _, k := range keys {
			out[Key(k, o)] = data[k]
		}
		j, _ := json.MarshalIndent(out, "", "  ")
		b.Write(append(j, '\n'))
	default:
		return "", fmt.Errorf("unknown format %q (want shell, dotenv, k8s, docker or json)", format)
	}
	return b.String(), nil
}

// ShellQuote single-quotes v for POSIX shells; newlines and $ stay literal.
func ShellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}

var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// EnvName returns k if it's a valid shell variable name, else UpperSnake(k).
func EnvName(k string) string {
	if envNameRe.MatchString(k) {
		return k
	}
	if s := UpperSnake(k); envNameRe.MatchString(s) {
		return s
	}
	return "_" + UpperSnake(k)
}

var k8sBad = regexp.MustCompile(`[^a-z0-9.-]+`)

// K8sName makes a valid DNS-1123 resource name.
func K8sName(s string) string {
	n := strings.Trim(k8sBad.ReplaceAllString(strings.ToLower(s), "-"), "-.")
	if n == "" {
		n = "secret"
	}
	return n[:min(len(n), 253)]
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

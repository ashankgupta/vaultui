// Package diff compares secrets key by key, with a char-level diff for long
// values. Pure; no Vault access.
package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

type Status byte

const (
	Same    Status = '='
	Added   Status = '+'
	Removed Status = '-'
	Changed Status = '~'
)

type Row struct {
	Status      Status
	Key         string
	Left, Right string
}

// Keys diffs left against right, sorted by key.
func Keys(left, right map[string]string) []Row {
	var rows []Row
	for k, l := range left {
		r, ok := right[k]
		switch {
		case !ok:
			rows = append(rows, Row{Removed, k, l, ""})
		case l != r:
			rows = append(rows, Row{Changed, k, l, r})
		default:
			rows = append(rows, Row{Same, k, l, r})
		}
	}
	for k, r := range right {
		if _, ok := left[k]; !ok {
			rows = append(rows, Row{Added, k, "", r})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows
}

// Summary renders e.g. "3 changed · 1 added · 2 removed".
func Summary(rows []Row) string {
	n := map[Status]int{}
	for _, r := range rows {
		n[r.Status]++
	}
	if n[Changed]+n[Added]+n[Removed] == 0 {
		return "identical"
	}
	return fmt.Sprintf("%d changed · %d added · %d removed", n[Changed], n[Added], n[Removed])
}

// NeedsCharDiff reports whether a changed value is worth a char-level diff.
func NeedsCharDiff(a, b string) bool {
	return len(a) > 80 || len(b) > 80 || strings.Contains(a, "\n") || strings.Contains(b, "\n")
}

func Chars(a, b string) []diffmatchpatch.Diff {
	dmp := diffmatchpatch.New()
	return dmp.DiffCleanupSemantic(dmp.DiffMain(a, b, false))
}

// EnvSuggestions swaps each environment segment in path for every other
// environment: "payments/prod/db" → "payments/dev/db", "payments/staging/db"...
func EnvSuggestions(path string, envs []string) []string {
	segs := strings.Split(path, "/")
	var out []string
	for i, s := range segs {
		for _, cur := range envs {
			if s != cur {
				continue
			}
			for _, other := range envs {
				if other == cur {
					continue
				}
				cp := append([]string(nil), segs...)
				cp[i] = other
				out = append(out, strings.Join(cp, "/"))
			}
		}
	}
	return out
}

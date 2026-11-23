// Package config is the entry point for reading LOTUS configuration: it loads
// the root file and its includes, resolves load-time constructs, validates the
// result against the directive registry, and compiles it into the typed model.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/compiler"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/loader"
	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/config/schema"
	"github.com/laikait/lotus/internal/config/validator"
)

// Result is a fully checked configuration.
type Result struct {
	*loader.Result
	// Config is the typed configuration the server runs on.
	Config *model.Config
	// Secrets holds the text of every value that came from env() or file().
	// It exists so output can be redacted; never print it.
	Secrets []string
}

// Check loads, validates and compiles the configuration rooted at path. The
// result is nil if any error was found; the diagnostics (errors, warnings and
// notes) are always returned.
func Check(path string, opts loader.Options) (*Result, []diag.Diagnostic) {
	res, diags := loader.Load(path, opts)
	if res == nil {
		return nil, diags
	}
	diags = append(diags, validator.Validate(res.File, schema.Default())...)
	if HasErrors(diags) {
		return nil, diags
	}
	cfg, more := compiler.Compile(res)
	diags = append(diags, more...)
	if cfg == nil || HasErrors(diags) {
		return nil, diags
	}
	return &Result{Result: res, Config: cfg, Secrets: collectSecrets(res.File)}, diags
}

// HasErrors reports whether any diagnostic is an error.
func HasErrors(diags []diag.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == diag.Error {
			return true
		}
	}
	return false
}

// ---- redacted output ----

const redacted = "<redacted>"

// ErrNoSuchSite is returned by InspectJSON when the requested site doesn't exist.
var ErrNoSuchSite = errors.New("no such site")

// InspectJSON renders the effective configuration as indented JSON. Values
// that came from env() or file() are replaced by "<redacted>", even when they
// are part of a longer string. If site is not empty, only that site is shown
// (matched case-insensitively against its name); ErrNoSuchSite is returned if
// there is none.
func (r *Result) InspectJSON(site string) ([]byte, error) {
	cfg := r.Config
	if site != "" {
		filtered := *cfg
		filtered.Sites = nil
		for _, s := range cfg.Sites {
			if s.Name == strings.ToLower(site) {
				filtered.Sites = append(filtered.Sites, s)
			}
		}
		if len(filtered.Sites) == 0 {
			return nil, ErrNoSuchSite
		}
		cfg = &filtered
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, err
	}
	// Encode without HTML escaping so the marker reads "<redacted>", not "<redacted>".
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(redactTree(tree, r.Secrets)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func redactTree(v any, secrets []string) any {
	switch x := v.(type) {
	case string:
		return redactString(x, secrets)
	case []any:
		for i := range x {
			x[i] = redactTree(x[i], secrets)
		}
		return x
	case map[string]any:
		for k, val := range x {
			x[k] = redactTree(val, secrets)
		}
		return x
	}
	return v
}

// redactString hides secrets inside s. Secrets are tried longest first so a
// short secret that is part of a longer one can't leave a fragment behind.
func redactString(s string, secrets []string) string {
	for _, sec := range secrets {
		if sec == "" {
			continue
		}
		if s == sec {
			return redacted
		}
		// Very short secrets would mangle ordinary text, so inside longer
		// strings only secrets of 4+ characters are replaced.
		if len(sec) >= 4 {
			s = strings.ReplaceAll(s, sec, redacted)
		}
	}
	return s
}

func collectSecrets(f *ast.File) []string {
	seen := map[string]bool{}
	var walkValue func(ast.Value)
	walkValue = func(v ast.Value) {
		switch x := v.(type) {
		case *ast.String:
			if x.Secret {
				if s, ok := x.Plain(); ok && s != "" {
					seen[s] = true
				}
			}
		case *ast.List:
			for _, item := range x.Items {
				walkValue(item)
			}
		}
	}
	var walk func([]ast.Statement)
	walk = func(stmts []ast.Statement) {
		for _, st := range stmts {
			switch s := st.(type) {
			case *ast.Directive:
				for _, a := range s.Args {
					walkValue(a)
				}
				if s.Block != nil {
					walk(s.Block.Statements)
				}
			case *ast.Pair:
				walkValue(s.Key)
				walkValue(s.Value)
			}
		}
	}
	walk(f.Statements)

	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

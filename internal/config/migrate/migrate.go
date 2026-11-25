// Package migrate upgrades a configuration file from an older language
// version to the current one (`lotus config migrate`).
//
// There is only one language version so far, so there are no steps. The
// mechanism is here so that the first incompatible change to the language
// ships with its migration: a step rewrites the syntax tree of a file from
// version N to N+1, and the file is printed again in the canonical style.
package migrate

import (
	"errors"
	"fmt"

	"github.com/laikait/lotus/internal/config/ast"
	"github.com/laikait/lotus/internal/config/diag"
	"github.com/laikait/lotus/internal/config/format"
	"github.com/laikait/lotus/internal/config/parser"
	"github.com/laikait/lotus/pkg/version"
)

// Step rewrites a file of language version From into version From+1. It
// changes the tree; the version line is updated by the caller.
type Step struct {
	From        int
	Description string
	Apply       func(f *ast.File) error
}

// Steps lists the migrations, one for each version change, in order. There are
// none yet.
var Steps []Step

// Errors returned by Source.
var (
	ErrSyntax     = errors.New("the file has syntax errors")
	ErrNoVersion  = errors.New("the file has no lotus_config_version line")
	ErrTooNew     = errors.New("the file is written for a newer LOTUS")
	ErrNoPath     = errors.New("there is no migration for this version")
	errBadVersion = errors.New("lotus_config_version needs one integer")
)

// Result describes what a migration did.
type Result struct {
	From, To int
	Applied  []string // descriptions of the steps that ran
	Output   []byte   // the migrated file (equal to the input, formatted, when nothing was needed)
	Changed  bool     // false when the file was already current
}

// Source migrates configuration text to the current language version.
func Source(name string, src []byte) (*Result, []diag.Diagnostic, error) {
	return migrate(name, src, version.ConfigLanguageVersion, Steps)
}

func migrate(name string, src []byte, target int, steps []Step) (*Result, []diag.Diagnostic, error) {
	f, diags := parser.Parse(name, src)
	for _, d := range diags {
		if d.Severity == diag.Error {
			return nil, diags, ErrSyntax
		}
	}
	v, err := versionOf(f)
	if err != nil {
		return nil, diags, err
	}
	res := &Result{From: v, To: target}
	switch {
	case v > target:
		return res, diags, fmt.Errorf("%w: it declares language version %d, and this build understands up to %d", ErrTooNew, v, target)
	case v == target:
		return res, diags, nil
	}
	for cur := v; cur < target; cur++ {
		var step *Step
		for i := range steps {
			if steps[i].From == cur {
				step = &steps[i]
			}
		}
		if step == nil {
			return res, diags, fmt.Errorf("%w: from version %d to %d", ErrNoPath, cur, cur+1)
		}
		if err := step.Apply(f); err != nil {
			return res, diags, fmt.Errorf("migrating from version %d: %w", cur, err)
		}
		res.Applied = append(res.Applied, step.Description)
		setVersion(f, cur+1)
	}
	out, err := format.File(f)
	if err != nil {
		return res, diags, err
	}
	res.Output, res.Changed = out, true
	return res, diags, nil
}

func versionDirective(f *ast.File) *ast.Directive {
	for _, s := range f.Statements {
		if d, ok := s.(*ast.Directive); ok && d.Name == "lotus_config_version" {
			return d
		}
	}
	return nil
}

func versionOf(f *ast.File) (int, error) {
	d := versionDirective(f)
	if d == nil {
		return 0, ErrNoVersion
	}
	if len(d.Args) != 1 {
		return 0, errBadVersion
	}
	n, ok := d.Args[0].(*ast.Int)
	if !ok || n.Value < 1 || n.Value > 1<<20 {
		return 0, errBadVersion
	}
	return int(n.Value), nil
}

func setVersion(f *ast.File, v int) {
	d := versionDirective(f)
	d.Args = []ast.Value{&ast.Int{Pos: d.Pos, Value: int64(v), Source: fmt.Sprint(v)}}
}

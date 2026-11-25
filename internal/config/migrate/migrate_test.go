package migrate

import (
	"errors"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/ast"
)

const v1 = "lotus_config_version 1\n\nserver {\n    listen \"http\" {\n        address \"127.0.0.1:8080\"\n    }\n}\n"

func TestCurrentVersionNeedsNothing(t *testing.T) {
	res, _, err := Source("m.lotus", []byte(v1))
	if err != nil || res.Changed || res.From != 1 || res.To != 1 || res.Output != nil {
		t.Errorf("%v %+v", err, res)
	}
}

func TestNewerVersionsAreRefused(t *testing.T) {
	_, _, err := Source("m.lotus", []byte("lotus_config_version 2\n"))
	if !errors.Is(err, ErrTooNew) || !strings.Contains(err.Error(), "up to 1") {
		t.Errorf("%v", err)
	}
}

func TestBrokenFiles(t *testing.T) {
	for name, tc := range map[string]struct {
		src  string
		want error
	}{
		"no version":     {"server {\n}\n", ErrNoVersion},
		"syntax":         {"lotus_config_version 1\nserver {\n", ErrSyntax},
		"version string": {"lotus_config_version \"1\"\n", errBadVersion},
		"two arguments":  {"lotus_config_version 1 2\n", errBadVersion},
		"zero":           {"lotus_config_version 0\n", errBadVersion},
	} {
		if _, _, err := Source("m.lotus", []byte(tc.src)); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}
}

// The mechanism, with made-up steps: a file at version 1 is taken to 3, each
// step runs once in order, the version line follows, comments survive.
func TestStepsRunInOrder(t *testing.T) {
	var ran []string
	steps := []Step{
		{From: 2, Description: "two to three", Apply: func(f *ast.File) error { ran = append(ran, "2"); return nil }},
		{From: 1, Description: "one to two", Apply: func(f *ast.File) error {
			ran = append(ran, "1")
			// rename a directive, as a real migration might
			for _, s := range f.Statements {
				if d, ok := s.(*ast.Directive); ok && d.Name == "server" {
					d.Name = "servers"
				}
			}
			return nil
		}},
	}
	src := "lotus_config_version 1\n\n# keep me\nserver {\n}\n"
	res, _, err := migrate("m.lotus", []byte(src), 3, steps)
	if err != nil {
		t.Fatal(err)
	}
	want := "lotus_config_version 3\n\n# keep me\nservers {\n}\n"
	if string(res.Output) != want || !res.Changed || strings.Join(ran, ",") != "1,2" || len(res.Applied) != 2 || res.From != 1 || res.To != 3 {
		t.Errorf("%q\n%+v ran=%v", res.Output, res, ran)
	}
}

func TestAMissingStepIsReported(t *testing.T) {
	_, _, err := migrate("m.lotus", []byte(v1), 3, []Step{{From: 2, Apply: func(*ast.File) error { return nil }}})
	if !errors.Is(err, ErrNoPath) || !strings.Contains(err.Error(), "from version 1 to 2") {
		t.Errorf("%v", err)
	}
}

func TestAFailingStepStops(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := migrate("m.lotus", []byte(v1), 2, []Step{{From: 1, Apply: func(*ast.File) error { return boom }}})
	if !errors.Is(err, boom) {
		t.Errorf("%v", err)
	}
}

func TestRealStepsFormAChain(t *testing.T) {
	// Every version below the current one has exactly one step (none yet).
	seen := map[int]bool{}
	for _, s := range Steps {
		if seen[s.From] || s.Apply == nil || s.Description == "" {
			t.Errorf("bad step %+v", s)
		}
		seen[s.From] = true
	}
}

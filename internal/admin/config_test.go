package admin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/control"
)

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestTheEditorReadsAndSavesFiles(t *testing.T) {
	e, _, cookie, csrf := siteEnv(t)

	rec := e.do("GET", "/v1/config/files", "", "", cookie)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"main.conf"`) {
		t.Fatalf("files: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/v1/config/file?name=main.conf", "", "", cookie)
	var got struct{ Content string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || !strings.Contains(got.Content, "lotus_config_version 1") {
		t.Fatalf("read: %d %s", rec.Code, rec.Body)
	}

	next := got.Content + "\n# a comment\n"
	if rec = e.do("POST", "/v1/config/validate", `{"name":"main.conf","content":`+jsonString(next)+`}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("validate: %d %s", rec.Code, rec.Body)
	}
	if e.backend.reloads != 0 {
		t.Error("validating reloaded the server")
	}
	if on, _ := e.srv.opts.Sites.ReadFile("main.conf"); on != got.Content {
		t.Error("validating changed the file")
	}

	if rec = e.do("PUT", "/v1/config/file", `{"name":"main.conf","content":`+jsonString(next)+`}`, csrf, cookie); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if on, _ := e.srv.opts.Sites.ReadFile("main.conf"); on != next || e.backend.reloads != 1 {
		t.Errorf("after save: reloads=%d", e.backend.reloads)
	}
}

func TestAnInvalidFileIsRefusedWithItsProblems(t *testing.T) {
	e, _, cookie, csrf := siteEnv(t)
	before, _ := e.srv.opts.Sites.ReadFile("main.conf")

	for _, path := range []struct{ method, url string }{{"POST", "/v1/config/validate"}, {"PUT", "/v1/config/file"}} {
		rec := e.do(path.method, path.url, `{"name":"main.conf","content":"this is not valid\n"}`, csrf, cookie)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"diagnostics"`) || !strings.Contains(rec.Body.String(), "would not be valid") {
			t.Errorf("%s: %d %s", path.url, rec.Code, rec.Body)
		}
	}
	if on, _ := e.srv.opts.Sites.ReadFile("main.conf"); on != before {
		t.Error("an invalid file changed the file")
	}
	if e.backend.reloads != 0 {
		t.Error("an invalid file was loaded")
	}
}

func TestARefusedReloadUndoesAnEdit(t *testing.T) {
	e, _, cookie, csrf := siteEnv(t)
	before, _ := e.srv.opts.Sites.ReadFile("main.conf")
	e.backend.reloadErr = control.NewError(control.CodeConfigInvalid, "cannot bind", nil)

	rec := e.do("PUT", "/v1/config/file", `{"name":"main.conf","content":`+jsonString(before+"\n# x\n")+`}`, csrf, cookie)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "undone") {
		t.Errorf("save: %d %s", rec.Code, rec.Body)
	}
	if on, _ := e.srv.opts.Sites.ReadFile("main.conf"); on != before {
		t.Error("the edit stayed after the server refused it")
	}
}

func TestTheEditorOnlyOpensItsOwnFilesAndOnlyForAdmins(t *testing.T) {
	e, dir, cookie, csrf := siteEnv(t)
	_ = os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o600)
	for _, name := range []string{"../secret.txt", "secret.txt", "sites.d/../main.conf", "/etc/passwd"} {
		if rec := e.do("GET", "/v1/config/file?name="+name, "", "", cookie); rec.Code != 404 {
			t.Errorf("read %q: %d", name, rec.Code)
		}
		if rec := e.do("PUT", "/v1/config/file", `{"name":`+jsonString(name)+`,"content":"x"}`, csrf, cookie); rec.Code != 404 {
			t.Errorf("save %q: %d", name, rec.Code)
		}
	}
	if rec := e.do("PUT", "/v1/config/file", `{"name":"main.conf","content":"a\u0000b"}`, csrf, cookie); rec.Code != 422 {
		t.Errorf("NUL: %d", rec.Code)
	}

	vcookie, vcsrf := e.signIn("vic", "another long secret")
	for _, c := range [][3]string{
		{"GET", "/v1/config/files", ""}, {"GET", "/v1/config/file?name=main.conf", ""},
		{"POST", "/v1/config/validate", `{"name":"main.conf","content":"x"}`}, {"PUT", "/v1/config/file", `{"name":"main.conf","content":"x"}`},
		{"GET", "/v1/logs", ""},
	} {
		if rec := e.do(c[0], c[1], c[2], vcsrf, vcookie); rec.Code != 403 {
			t.Errorf("viewer %s %s: %d", c[0], c[1], rec.Code)
		}
	}
}

func TestLogsAreShownByIDWithAFilter(t *testing.T) {
	e := newEnv(t)
	cookie, _ := e.signIn("ada", "correct horse battery")
	logFile := filepath.Join(t.TempDir(), "access.log")
	var b strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&b, "line %d GET /page%d\n", i, i)
	}
	if err := os.WriteFile(logFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	e.srv.opts.Logs = func() []LogSource {
		return []LogSource{{ID: "site.a.test.access", Name: "a.test access log", Path: logFile}}
	}

	rec := e.do("GET", "/v1/logs", "", "", cookie)
	if !strings.Contains(rec.Body.String(), "site.a.test.access") || strings.Contains(rec.Body.String(), logFile) {
		t.Errorf("list (the path must not be sent): %s", rec.Body)
	}

	var out struct{ Lines []string }
	rec = e.do("GET", "/v1/logs/site.a.test.access?lines=5", "", "", cookie)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Lines) != 5 || out.Lines[4] != "line 50 GET /page50" {
		t.Errorf("tail: %v", out.Lines)
	}
	rec = e.do("GET", "/v1/logs/site.a.test.access?filter=PAGE4", "", "", cookie)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Lines) != 11 { // page4, page40 ... page49
		t.Errorf("filter: %d lines %v", len(out.Lines), out.Lines)
	}

	// An id that is not in the list, or a path, is not found: the page never names a file.
	for _, id := range []string{"nope", "..%2Fmain.conf", "C%3A%5Cwindows%5Cwin.ini"} {
		if rec = e.do("GET", "/v1/logs/"+id, "", "", cookie); rec.Code != 404 {
			t.Errorf("log %q: %d", id, rec.Code)
		}
	}
}

func TestTailFileOfALargeFileReadsOnlyTheEnd(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.log")
	var b strings.Builder
	for i := 0; i < 40000; i++ {
		fmt.Fprintf(&b, "entry number %06d with some padding to make the line longer\n", i)
	}
	_ = os.WriteFile(p, []byte(b.String()), 0o644)
	lines, size, err := tailFile(p, 3, "")
	if err != nil || size != int64(b.Len()) || len(lines) != 3 || !strings.Contains(lines[2], "039999") {
		t.Fatalf("tail: %v %d %v", err, size, lines)
	}
	if long, _, _ := tailFile(p, 5, ""); len(long) != 5 || strings.HasPrefix(long[0], "ntry") {
		t.Errorf("a cut line was returned: %q", long)
	}
}

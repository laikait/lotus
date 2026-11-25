package sitefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditorOnlyOpensTheMainFileAndSiteFiles(t *testing.T) {
	st, dir := newStore(t)
	_ = os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("x"), 0o600)
	for _, name := range []string{"../secret.txt", "secret.txt", "sites.d/../main.conf", "sites.d/sub/a.conf", "/etc/passwd", `C:\Windows\win.ini`, "sites.d/a.conf.bak", "sites.d/.hidden.conf", ""} {
		if _, err := st.ReadFile(name); !errors.Is(err, ErrBadName) {
			t.Errorf("ReadFile(%q): %v", name, err)
		}
	}
	text, err := st.ReadFile("main.conf")
	if err != nil || !strings.Contains(text, "lotus_config_version 1") {
		t.Fatalf("main.conf: %v %q", err, text)
	}
}

func TestFilesListsTheMainFileAndSites(t *testing.T) {
	st, dir := newStore(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))
	if _, err := st.Add(Spec{Name: "a.test", Root: web}, false); err != nil {
		t.Fatal(err)
	}
	files, err := st.Files()
	if err != nil || len(files) != 2 || files[0].Name != "main.conf" || files[1].Name != "sites.d/a.test.conf" || !files[1].Managed {
		t.Fatalf("files: %+v %v", files, err)
	}
}

func TestCheckFileKeepsNothing(t *testing.T) {
	st, _ := newStore(t)
	before, _ := st.ReadFile("main.conf")

	if _, err := st.CheckFile("main.conf", before+"\n# a comment\n"); err != nil {
		t.Fatalf("a valid file was refused: %v", err)
	}
	diags, err := st.CheckFile("main.conf", before+"\nthis is not valid\n")
	var invalid *InvalidError
	if !errors.As(err, &invalid) || len(diags) == 0 {
		t.Fatalf("an invalid file was accepted: %v", err)
	}
	if after, _ := st.ReadFile("main.conf"); after != before {
		t.Error("checking changed the file")
	}
}

func TestSaveFileKeepsABackupAndRefusesAnInvalidFile(t *testing.T) {
	st, dir := newStore(t)
	before, _ := st.ReadFile("main.conf")

	next := before + "\n# edited in the admin\n"
	old, err := st.SaveFile("main.conf", next)
	if err != nil || string(old) != before {
		t.Fatalf("save: %v", err)
	}
	if got, _ := st.ReadFile("main.conf"); got != next {
		t.Error("the new text was not saved")
	}
	if bak, _ := os.ReadFile(filepath.Join(dir, "main.conf.bak")); string(bak) != before {
		t.Error("the old text was not kept as .bak")
	}

	if _, err := st.SaveFile("main.conf", "nonsense {{{"); err == nil {
		t.Fatal("an invalid file was saved")
	}
	if got, _ := st.ReadFile("main.conf"); got != next {
		t.Error("a refused save changed the file")
	}

	st.RestoreFile("main.conf", old)
	if got, _ := st.ReadFile("main.conf"); got != before {
		t.Error("restore did not put the old text back")
	}
}

func TestContentLimits(t *testing.T) {
	st, _ := newStore(t)
	if _, err := st.SaveFile("main.conf", strings.Repeat("#", MaxFileSize+1)); !errors.Is(err, ErrTooBig) {
		t.Errorf("too big: %v", err)
	}
	if _, err := st.SaveFile("main.conf", "a\x00b"); !errors.Is(err, ErrBadContent) {
		t.Errorf("NUL: %v", err)
	}
	if _, err := st.SaveFile("main.conf", "\xff\xfe"); !errors.Is(err, ErrBadContent) {
		t.Errorf("invalid UTF-8: %v", err)
	}
}

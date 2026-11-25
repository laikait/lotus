package sitefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mainConf = `lotus_config_version 1

server {
    listen "http" {
        address "127.0.0.1:8080"
    }

    include "sites.d/*.conf"
}
`

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "main.conf")
	if err := os.WriteFile(cfg, []byte(mainConf), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Store{Config: cfg}, dir
}

func TestRenderAndParseRoundTrip(t *testing.T) {
	sp := Normalize(Spec{Name: "Example.com", Aliases: []string{"WWW.example.com", "*.cdn.example.com"}, Root: "C:\\www\\ex", Index: []string{"index.php", "index.html"}})
	if sp.Root != "C:/www/ex" || sp.Name != "example.com" || sp.Aliases[0] != "www.example.com" {
		t.Fatalf("normalize: %+v", sp)
	}
	got, ok := Parse(Render(sp))
	if !ok {
		t.Fatal("a file made here can't be read back")
	}
	if got.Name != sp.Name || got.Root != sp.Root || strings.Join(got.Aliases, ",") != strings.Join(sp.Aliases, ",") || strings.Join(got.Index, ",") != "index.php,index.html" || got.TLS != "off" {
		t.Errorf("round trip: %+v", got)
	}
}

func TestParseRefusesAFileEditedByHand(t *testing.T) {
	good := Render(Normalize(Spec{Name: "a.test", Root: "/srv/a"}))
	for name, text := range map[string]string{
		"no marker":   strings.Replace(good, Marker, "# mine", 1),
		"extra route": strings.Replace(good, "    tls off\n", "    tls off\n    route \"/\" {\n    }\n", 1),
		"comment out": strings.Replace(good, "    root", "    # root", 1),
	} {
		if _, ok := Parse(text); ok {
			t.Errorf("%s: parsed as editable", name)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Normalize(Spec{Name: "ok.test", Root: "/srv/ok"})
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]Spec{
		"name with slash":  {Name: "../evil", Root: "/srv/x"},
		"name with space":  {Name: "a b", Root: "/srv/x"},
		"alias":            {Name: "a.test", Aliases: []string{"x y"}, Root: "/srv/x"},
		"dup alias":        {Name: "a.test", Aliases: []string{"a.test"}, Root: "/srv/x"},
		"relative root":    {Name: "a.test", Root: "www/x"},
		"dotdot root":      {Name: "a.test", Root: "/srv/../etc"},
		"quote in root":    {Name: "a.test", Root: "/srv/\"x"},
		"newline in root":  {Name: "a.test", Root: "/srv/x\n}"},
		"brace in root":    {Name: "a.test", Root: "/srv/{x}"},
		"index with slash": {Name: "a.test", Root: "/srv/x", Index: []string{"a/b.html"}},
		"tls":              {Name: "a.test", Root: "/srv/x", TLS: "manual"},
	}
	for name, sp := range bad {
		if err := Validate(Normalize(sp)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAddUpdateRemove(t *testing.T) {
	st, dir := newStore(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))

	sp, err := st.Add(Spec{Name: "Example.com", Aliases: []string{"www.example.com"}, Root: web}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "web", "index.html")); err != nil {
		t.Errorf("no sample page: %v", err)
	}
	if _, err := st.Add(Spec{Name: "example.com", Root: web}, false); !errors.Is(err, ErrExists) {
		t.Errorf("second add: %v", err)
	}

	list, _ := st.List()
	if len(list) != 1 || !list[0].Editable || list[0].Name != sp.Name || list[0].Root != web {
		t.Fatalf("list: %+v", list)
	}

	if _, err := st.Update("example.com", Spec{Aliases: []string{"www.example.com", "shop.example.com"}, Root: web, Index: []string{"index.php"}}); err != nil {
		t.Fatal(err)
	}
	list, _ = st.List()
	if len(list[0].Aliases) != 2 || list[0].Index[0] != "index.php" {
		t.Errorf("after update: %+v", list[0])
	}
	if _, err := st.Update("nothing.test", Spec{Root: web}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of a missing site: %v", err)
	}

	if err := st.Remove("example.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.Remove("example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "web", "index.html")); err != nil {
		t.Error("the web directory was touched")
	}
}

func TestAnInvalidConfigurationIsRolledBack(t *testing.T) {
	st, dir := newStore(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))
	if _, err := st.Add(Spec{Name: "one.test", Root: web}, false); err != nil {
		t.Fatal(err)
	}
	// "automatic" TLS needs an https listener and ACME: this configuration has neither, so it is refused.
	before := st.Read("one.test")
	_, err := st.Update("one.test", Spec{Root: web, TLS: "automatic"})
	var invalid *InvalidError
	if !errors.As(err, &invalid) || len(invalid.Diagnostics) == 0 {
		t.Fatalf("update: %v", err)
	}
	if string(st.Read("one.test")) != string(before) {
		t.Error("the old file was not put back")
	}
	// A new site that is invalid leaves no file.
	if _, err := st.Add(Spec{Name: "two.test", Root: web, TLS: "automatic"}, false); err == nil {
		t.Fatal("an invalid new site was kept")
	}
	if _, err := os.Stat(st.File("two.test")); err == nil {
		t.Error("the file of the refused site is still there")
	}
	// No temporary files are left in the folder.
	entries, _ := os.ReadDir(st.Dir())
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".site-") {
			t.Errorf("left over: %s", e.Name())
		}
	}
}

func TestAFileEditedByHandIsNeverRewritten(t *testing.T) {
	st, dir := newStore(t)
	web := filepath.ToSlash(filepath.Join(dir, "web"))
	hand := "site \"hand.test\" {\n    root \"" + web + "\"\n    index [ \"index.html\" ]\n    tls off\n}\n"
	if err := os.MkdirAll(st.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.File("hand.test"), []byte(hand), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update("hand.test", Spec{Root: web}); !errors.Is(err, ErrNotEditable) {
		t.Errorf("update: %v", err)
	}
	if string(st.Read("hand.test")) != hand {
		t.Error("the hand-made file was changed")
	}
	list, _ := st.List()
	if len(list) != 1 || list[0].Editable {
		t.Errorf("list: %+v", list)
	}
}

func TestRemoveRefusesANameThatIsAPath(t *testing.T) {
	st, _ := newStore(t)
	if err := st.Remove("../main"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove ../main: %v", err)
	}
}

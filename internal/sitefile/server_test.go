package sitefile

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestServerFileRoundTripAndCheck(t *testing.T) {
	st, _ := newStore(t)
	sp := ServerSpec{
		PHP:   []PHPPoolSpec{{Name: "app", Command: "C:\\xampp\\php\\php-cgi.exe", Processes: 4, MaxRequests: 500, Timeout: 30}, {Name: "legacy", Command: "php-cgi", Processes: 2}},
		HTTPS: &HTTPSSpec{Email: "ops@example.com", HTTPS: "0.0.0.0:443", HTTP: "0.0.0.0:80", Staging: true},
	}
	old, err := st.SaveServer(sp)
	if err != nil {
		var inv *InvalidError
		if errors.As(err, &inv) {
			t.Fatalf("%v %+v", err, inv.Diagnostics)
		}
		t.Fatal(err)
	}
	if old != nil {
		t.Errorf("there was no earlier file: %q", old)
	}
	got, editable, err := st.Server()
	if err != nil || !editable || len(got.PHP) != 2 || got.PHP[0].Command != "C:/xampp/php/php-cgi.exe" || got.PHP[1].Timeout != 60 || got.HTTPS == nil || !got.HTTPS.Staging {
		t.Fatalf("read back: %+v editable=%v %v", got, editable, err)
	}
	text := string(st.Read(ServerFile))
	for _, want := range []string{`php_backend "app"`, `max_requests 500`, `timeout 30s`, `listen "lotus-https"`, `protocol https`, `acme {`, `staging`} {
		if want == "staging" {
			want = "acme-staging-v02"
		}
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	// It is not a site, but the editor can open it, and no site can take its name.
	if list, _ := st.List(); len(list) != 0 {
		t.Errorf("the server file is listed as a site: %+v", list)
	}
	files, _ := st.Files()
	found := false
	for _, f := range files {
		found = found || f.Name == "sites.d/"+ServerFile+".conf"
	}
	if !found {
		t.Errorf("the editor does not list it: %+v", files)
	}
	if _, err := st.Add(Spec{Name: ServerFile, Root: "/srv/x"}, false); err == nil {
		t.Error("a site took the reserved name")
	}
	if err := st.Remove(ServerFile); err == nil {
		t.Error("Remove deleted the server file")
	}

	// Saying nothing removes the file.
	if _, err := st.SaveServer(ServerSpec{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(st.File(ServerFile)); err == nil {
		t.Error("the empty spec left a file")
	}
}

func TestServerFileValues(t *testing.T) {
	st, _ := newStore(t)
	good := PHPPoolSpec{Name: "app", Command: "php-cgi", Processes: 2}
	for name, sp := range map[string]ServerSpec{
		"pool name":      {PHP: []PHPPoolSpec{{Name: `a"b`, Command: "php-cgi", Processes: 1}}},
		"duplicate":      {PHP: []PHPPoolSpec{good, good}},
		"command quote":  {PHP: []PHPPoolSpec{{Name: "a", Command: `/x/php"; evil`, Processes: 1}}},
		"command spaces": {PHP: []PHPPoolSpec{{Name: "a", Command: "php cgi", Processes: 1}}},
		"relative path":  {PHP: []PHPPoolSpec{{Name: "a", Command: "bin/php-cgi", Processes: 1}}},
		"processes":      {PHP: []PHPPoolSpec{{Name: "a", Command: "php-cgi", Processes: 65}}},
		"timeout":        {PHP: []PHPPoolSpec{{Name: "a", Command: "php-cgi", Processes: 1, Timeout: 601}}},
		"ini":            {PHP: []PHPPoolSpec{{Name: "a", Command: "php-cgi", Processes: 1, PHPIni: "php.ini"}}},
		"email":          {HTTPS: &HTTPSSpec{Email: "not an email"}},
		"email quote":    {HTTPS: &HTTPSSpec{Email: `a"@b.com`}},
		"https address":  {HTTPS: &HTTPSSpec{Email: "a@b.com", HTTPS: "443"}},
		"port":           {HTTPS: &HTTPSSpec{Email: "a@b.com", HTTPS: "0.0.0.0:99999"}},
		"same address":   {HTTPS: &HTTPSSpec{Email: "a@b.com", HTTPS: "0.0.0.0:443", HTTP: "0.0.0.0:443"}},
	} {
		if _, err := st.SaveServer(sp); err == nil {
			t.Errorf("%s: accepted", name)
		} else {
			var bad *ValidationError
			if !errors.As(err, &bad) {
				t.Errorf("%s: %v is not a ValidationError", name, err)
			}
		}
	}
}

func TestAHandEditedServerFileIsLeftAlone(t *testing.T) {
	st, _ := newStore(t)
	if _, err := st.SaveServer(ServerSpec{PHP: []PHPPoolSpec{{Name: "app", Command: "php-cgi", Processes: 2}}}); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(st.Read(ServerFile)), "processes 2", "processes 9", 1)
	if err := os.WriteFile(st.File(ServerFile), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, editable, _ := st.Server(); editable {
		t.Error("a hand-edited file counts as editable")
	}
	if _, err := st.SaveServer(ServerSpec{}); !errors.Is(err, ErrServerFileByHand) {
		t.Errorf("SaveServer over a hand-edited file: %v", err)
	}
	if got, _ := os.ReadFile(st.File(ServerFile)); string(got) != edited {
		t.Error("the hand-edited file was changed")
	}
}

// A pool that a site uses can't be removed: the whole-configuration check says so and the file stays.
func TestRemovingAPoolASiteUsesIsRefused(t *testing.T) {
	st, dir := newStore(t)
	if _, err := st.SaveServer(ServerSpec{PHP: []PHPPoolSpec{{Name: "app", Command: "php-cgi", Processes: 2}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Spec{Name: "p.test", Root: strings.ReplaceAll(dir, `\`, "/") + "/web", Template: "php", PHPBackend: "app"}, true); err != nil {
		t.Fatal(err)
	}
	_, err := st.SaveServer(ServerSpec{})
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("removing the pool: %v", err)
	}
	if _, e := os.Stat(st.File(ServerFile)); e != nil {
		t.Error("the file was removed although the check failed")
	}
}

// With the HTTPS wizard's listener present, a plain-HTTP site must be limited to the http listener (or use TLS).
func TestHTTPSListenerAndPlainSites(t *testing.T) {
	st, dir := newStore(t)
	root := strings.ReplaceAll(dir, `\`, "/") + "/web"
	if _, err := st.Add(Spec{Name: "plain.test", Root: root}, true); err != nil {
		t.Fatal(err)
	}
	_, err := st.SaveServer(ServerSpec{HTTPS: &HTTPSSpec{Email: "ops@example.com"}})
	var inv *InvalidError
	if !errors.As(err, &inv) {
		t.Fatalf("a tls-off site on the https listener should be refused: %v", err)
	}
	if _, err := st.Update("plain.test", Spec{Root: root, Listeners: []string{"http"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveServer(ServerSpec{HTTPS: &HTTPSSpec{Email: "ops@example.com"}}); err != nil {
		t.Fatalf("after limiting the site to the http listener: %v", err)
	}
	if got := st.ListenerNames(); strings.Join(got, ",") != "http,lotus-https" {
		t.Errorf("listener names: %v", got)
	}
	if _, err := st.Update("plain.test", Spec{Root: root, Listeners: []string{`a"b`}}); err == nil {
		t.Error("a bad listener name was accepted")
	}
}

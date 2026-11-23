package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodConfig = `lotus_config_version 1
server {
    listen "http" { address "0.0.0.0:8080" }
    site "localhost" { root "/var/www" }
}
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.lotus")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestConfigValidateOK(t *testing.T) {
	code, out, errOut := runCLI("config", "validate", writeConfig(t, goodConfig))
	if code != exitOK || !strings.Contains(out, "configuration is valid") || errOut != "" {
		t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestConfigValidateInvalid(t *testing.T) {
	bad := strings.Replace(goodConfig, `root "/var/www"`, `rot "/var/www"`, 1)
	code, out, errOut := runCLI("config", "validate", writeConfig(t, bad))
	if code != exitInvalidConfig {
		t.Errorf("exit code = %d, want %d", code, exitInvalidConfig)
	}
	if out != "" {
		t.Errorf("stdout should be empty, got %q", out)
	}
	for _, want := range []string{":4:", "error [LOTUS-CFG-0120]", `unknown directive "rot"`, `hint: did you mean "root"?`, "configuration is invalid (1 error(s))"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut)
		}
	}
}

func TestConfigValidateJSON(t *testing.T) {
	code, out, _ := runCLI("config", "validate", "--json", writeConfig(t, goodConfig))
	if code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	var rep struct {
		Valid       bool              `json:"valid"`
		Version     int               `json:"config_version"`
		Files       []string          `json:"files"`
		Diagnostics []json.RawMessage `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if !rep.Valid || rep.Version != 1 || len(rep.Files) != 1 || rep.Diagnostics == nil || len(rep.Diagnostics) != 0 {
		t.Errorf("report = %+v (diagnostics must be an empty array, not null)", rep)
	}

	bad := strings.Replace(goodConfig, `"0.0.0.0:8080"`, `"nonsense"`, 1)
	code, out, _ = runCLI("config", "validate", "--json", writeConfig(t, bad))
	if code != exitInvalidConfig {
		t.Fatalf("exit code = %d", code)
	}
	var bad2 struct {
		Valid       bool `json:"valid"`
		Diagnostics []struct {
			Severity string `json:"severity"`
			Code     string `json:"code"`
			Message  string `json:"message"`
			Pos      struct {
				Line   int `json:"line"`
				Column int `json:"column"`
			} `json:"pos"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal([]byte(out), &bad2); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if bad2.Valid || len(bad2.Diagnostics) != 1 {
		t.Fatalf("report = %+v", bad2)
	}
	if d := bad2.Diagnostics[0]; d.Severity != "error" || d.Code != "LOTUS-CFG-0125" || d.Pos.Line != 3 {
		t.Errorf("diagnostic = %+v", d)
	}
}

func TestConfigValidateMissingFile(t *testing.T) {
	code, _, errOut := runCLI("config", "validate", filepath.Join(t.TempDir(), "nope.lotus"))
	if code != exitInvalidConfig || !strings.Contains(errOut, "cannot read configuration file") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
}

func TestConfigUsageErrors(t *testing.T) {
	tests := [][]string{
		{"config"},
		{"config", "frobnicate"},
		{"config", "validate"},
		{"config", "validate", "a.lotus", "b.lotus"},
		{"config", "validate", "--nope", "a.lotus"},
	}
	for _, args := range tests {
		if code, _, _ := runCLI(args...); code != exitUsage {
			t.Errorf("%v: exit code = %d, want %d", args, code, exitUsage)
		}
	}
}

func TestConfigValidateDoesNotPrintSecrets(t *testing.T) {
	t.Setenv("LOTUS_TEST_SECRET", "hunter2-do-not-print")
	bad := strings.Replace(goodConfig, `address "0.0.0.0:8080"`, `address env("LOTUS_TEST_SECRET")`, 1)
	code, out, errOut := runCLI("config", "validate", "--json", writeConfig(t, bad))
	if code != exitInvalidConfig {
		t.Fatalf("exit code = %d", code)
	}
	if strings.Contains(out+errOut, "hunter2") {
		t.Errorf("secret leaked:\n%s\n%s", out, errOut)
	}
}

const inspectConfig = `lotus_config_version 1
server {
    listen "http" { address "0.0.0.0:8080" }
    tls { acme { email env("LOTUS_TEST_EMAIL") } }
    site "one.test" { root "/srv/one"
 tls off }
    site "two.test" { root "/srv/two"
 tls off }
}
`

func TestConfigInspect(t *testing.T) {
	t.Setenv("LOTUS_TEST_EMAIL", "private@example.com")
	path := writeConfig(t, inspectConfig)

	code, out, errOut := runCLI("config", "inspect", path)
	if code != exitOK || errOut != "" {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
	var tree struct {
		Sites []struct{ Name, Root string } `json:"sites"`
		TLS   struct {
			ACME struct{ Email string } `json:"acme"`
		} `json:"tls"`
		Limits struct {
			MaxRequestBody int64 `json:"max_request_body"`
		} `json:"limits"`
	}
	if err := json.Unmarshal([]byte(out), &tree); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if len(tree.Sites) != 2 || tree.Limits.MaxRequestBody != 10<<20 {
		t.Errorf("sites/defaults wrong: %+v", tree)
	}
	if strings.Contains(out, "private@example.com") || tree.TLS.ACME.Email != "<redacted>" {
		t.Errorf("secret not redacted (email=%q):\n%s", tree.TLS.ACME.Email, out)
	}

	code, out, _ = runCLI("config", "inspect", "--site", "TWO.test", path)
	if code != exitOK || !strings.Contains(out, "two.test") || strings.Contains(out, "one.test") {
		t.Errorf("--site filter: code=%d\n%s", code, out)
	}
	if code, _, errOut = runCLI("config", "inspect", "--site", "nope.test", path); code != exitUsage || !strings.Contains(errOut, "no site named") {
		t.Errorf("unknown site: code=%d stderr=%q", code, errOut)
	}
}

func TestConfigInspectInvalid(t *testing.T) {
	bad := strings.Replace(inspectConfig, `root "/srv/one"`, `rot "/srv/one"`, 1)
	code, out, errOut := runCLI("config", "inspect", writeConfig(t, bad))
	if code != exitInvalidConfig || out != "" || !strings.Contains(errOut, "nothing to inspect") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if code, _, _ := runCLI("config", "inspect"); code != exitUsage {
		t.Errorf("missing file argument: code=%d", code)
	}
}

func TestConfigValidateRunsCompileChecks(t *testing.T) {
	// Two sites claim the same hostname: only the compile step can see that.
	dup := `lotus_config_version 1
server {
    listen "http" { address "0.0.0.0:8080" }
    site "a.test" { tls off }
    site "b.test" { aliases [ "a.test" ]
 tls off }
}
`
	code, _, errOut := runCLI("config", "validate", writeConfig(t, dup))
	if code != exitInvalidConfig || !strings.Contains(errOut, "LOTUS-CFG-0141") {
		t.Errorf("code=%d stderr=%q", code, errOut)
	}
}

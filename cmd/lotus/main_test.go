package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/laikait/lotus/pkg/version"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args", nil, exitUsage, "", "Usage:"},
		{"help", []string{"help"}, exitOK, "Usage:", ""},
		{"version", []string{"version"}, exitOK, "lotus " + version.Version, ""},
		{"version bad flag", []string{"version", "--yaml"}, exitUsage, "", "usage: lotus version"},
		{"unknown", []string{"frobnicate"}, exitUsage, "", `unknown command "frobnicate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if tt.wantStdout != "" && !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && !strings.Contains(errOut.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantStderr)
			}
		})
	}
}

func TestVersionJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version", "--json"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, errOut.String())
	}
	var info version.Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if info.ConfigLanguage != version.ConfigLanguageVersion || info.ControlProtocol != version.ControlProtocol {
		t.Errorf("unexpected info: %+v", info)
	}
}

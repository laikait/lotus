package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/config/diag"
)

const metricsServer = `lotus_config_version 1
server {
    listen "http" { address "127.0.0.1:8080" }
%s
    site "example.com" {
        root "/srv/a"
%s
    }
}`

func metricsConf(block, site string) string {
	return portable(strings.Replace(strings.Replace(metricsServer, "%s", block, 1), "%s", site, 1))
}

func TestCompileMetricsSettings(t *testing.T) {
	c := compile(t, metricsConf(`    metrics {
        address "10.1.2.3:9180"
        path "/internal/metrics"
        bearer_token "tok-xyz"
        allow [ "10.0.0.0/8", "192.0.2.7" ]
    }`, ""))
	m := c.Metrics
	if m == nil || m.Address != "10.1.2.3:9180" || m.Path != "/internal/metrics" || string(m.BearerToken) != "tok-xyz" || len(m.Allow) != 2 ||
		m.Allow[0].String() != "10.0.0.0/8" || m.Allow[1].String() != "192.0.2.7/32" {
		t.Errorf("%+v", m)
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "tok-xyz") {
		t.Errorf("the bearer token is in the JSON form: %s", b)
	}

	c = compile(t, metricsConf(`    metrics { address "127.0.0.1:9180" }`, ""))
	if c.Metrics == nil || c.Metrics.Path != "/metrics" || c.Metrics.BearerToken != "" || len(c.Metrics.Allow) != 0 {
		t.Errorf("defaults: %+v", c.Metrics)
	}
	if c := compile(t, metricsConf("", "")); c.Metrics != nil {
		t.Errorf("metrics are on without a block: %+v", c.Metrics)
	}
}

func TestLoopbackMetricsNeedNoProtection(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9180", "[::1]:9180", "localhost:9180", "127.5.5.5:9180"} {
		compile(t, metricsConf(`    metrics { address "`+addr+`" }`, ""))
	}
}

func TestMetricsErrors(t *testing.T) {
	for name, tc := range map[string]struct{ block, code, text string }{
		"reachable and open":   {`metrics { address "0.0.0.0:9180" }`, diag.CodeInvalidCombination, "nothing protects it"},
		"a private address":    {`metrics { address "10.0.0.5:9180" }`, diag.CodeInvalidCombination, "bearer_token or allow"},
		"all interfaces":       {`metrics { address ":9180" }`, diag.CodeInvalidCombination, "nothing protects it"},
		"no address":           {`metrics { path "/m" }`, "", "address"},
		"path without a slash": {"metrics { address \"127.0.0.1:9180\"\n path \"metrics\" }", diag.CodeInvalidValue, "must start with /"},
		"path with a query":    {"metrics { address \"127.0.0.1:9180\"\n path \"/m?x=1\" }", diag.CodeInvalidValue, "must start with /"},
		"bad address":          {`metrics { address "nope" }`, "", ""},
		"bad network":          {"metrics { address \"127.0.0.1:9180\"\n allow [ \"not-a-network\" ] }", "", ""},
		"metrics twice":        {"metrics { address \"127.0.0.1:9180\" }\n metrics { address \"127.0.0.1:9181\" }", "", ""},
	} {
		_, diags := Check("main.lotus", optsFor(map[string]string{"main.lotus": metricsConf("    "+tc.block, "")}))
		out := format(diags)
		if len(errorsOf(diags)) == 0 || !strings.Contains(out, tc.code) || !strings.Contains(out, tc.text) {
			t.Errorf("%s: want %s %q in:\n%s", name, tc.code, tc.text, out)
		}
	}
	// Either protection is enough.
	compile(t, metricsConf("    metrics { address \"0.0.0.0:9180\"\n bearer_token \"x\" }", ""))
	compile(t, metricsConf("    metrics { address \"0.0.0.0:9180\"\n allow [ \"10.0.0.0/8\" ] }", ""))
}

func TestMetricsAreAdminOnly(t *testing.T) {
	files := map[string]string{
		"main.lotus":   metricsConf("", `include "tenant.lotus"`),
		"tenant.lotus": "metrics { address \"127.0.0.1:9\" }",
	}
	_, diags := Check("main.lotus", optsFor(files))
	if len(errorsOf(diags)) == 0 {
		t.Errorf("a tenant file opened a metrics listener:\n%s", format(diags))
	}
}

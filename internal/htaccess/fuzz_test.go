package htaccess

import (
	"strings"
	"testing"
	"time"
)

// FuzzHtaccess feeds arbitrary bytes through the whole pipeline. A .htaccess
// file is written by a tenant, so it is untrusted input: whatever it holds, the
// parser, the compiler and the evaluator must neither panic nor run long.
func FuzzHtaccess(f *testing.F) {
	for _, seed := range []string{
		wordpress, laravel, drupalish,
		"Redirect /a /b\nRedirectMatch ^/x/(.*)$ /y/$1\nErrorDocument 404 \"x\"\nHeader set A b\n",
		"Require ip 10.0.0.0/8\n<RequireAll>\nRequire not ip 1.2.3.4\n</RequireAll>\nOrder deny,allow\n",
		"RewriteEngine On\nRewriteCond %{HTTP:X} (.*) [OR,NC]\nRewriteRule ^(.*)$ /$1?%1 [QSA,R=301,E=A:%{REQUEST_URI},S=2,C]\n",
		"<Files \"a*\">\n<IfModule !x>\nHeader unset A\n</IfModule>\n</Files>\n",
		"\\n\\n", "<", "</>", "\"", "RewriteRule", "<IfModule mod_rewrite.c>",
	} {
		f.Add([]byte(seed))
	}
	fs := fakeFS{"/": true, "/a": true, "/a/b.php": false}
	f.Fuzz(func(t *testing.T, data []byte) {
		start := time.Now()
		nodes, problems := Parse(data)
		c := Compile(nodes, problems, Options{})
		layers := []Layer{{"/", c}, {"/a/", c}}
		for _, path := range []string{"/", "/a/", "/a/b.php", "/x/" + strings.Repeat("y", 300), "/%00"} {
			for _, noRewrite := range []bool{false, true} {
				Evaluate(layers, newReq(fs, path, "q=1&r=2"), noRewrite)
			}
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("took %v for %d bytes", d, len(data))
		}
	})
}

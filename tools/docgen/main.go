// Command docgen writes the configuration reference from the directive
// registry: go run ./tools/docgen docs/configuration/directives
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/laikait/lotus/internal/config/schema"
	"github.com/laikait/lotus/internal/docgen"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: docgen <output directory>")
		os.Exit(1)
	}
	dir := os.Args[1]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pages := docgen.Generate(schema.Default())
	// Remove pages of directives that no longer exist.
	old, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	for _, f := range old {
		if _, ok := pages[filepath.Base(f)]; !ok {
			_ = os.Remove(f)
		}
	}
	for name, text := range pages {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Printf("wrote %d pages to %s\n", len(pages), dir)
}

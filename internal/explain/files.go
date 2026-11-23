package explain

import (
	"strings"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/laikait/lotus/internal/fspath"
	"github.com/laikait/lotus/internal/staticfiles"
)

// DiskFiles checks files on the local disk with the same resolver, and so the
// same traversal, dotfile and symlink rules, as the server. Close it when done.
type DiskFiles struct {
	resolvers map[string]*fspath.Resolver
	handlers  map[string]*staticfiles.Handler
	failed    map[string]bool
}

// NewDiskFiles returns a Files that reads document roots from disk.
func NewDiskFiles() *DiskFiles {
	return &DiskFiles{
		resolvers: map[string]*fspath.Resolver{},
		handlers:  map[string]*staticfiles.Handler{},
		failed:    map[string]bool{},
	}
}

// Handler implements Files. A root that can't be opened (for example, the
// configuration is being checked on another machine) gives an error.
func (d *DiskFiles) Handler(site *model.Site, root string, index []string) (*staticfiles.Handler, error) {
	key := site.Name + "\x00" + root + "\x00" + strings.Join(index, "\x00")
	if h := d.handlers[key]; h != nil {
		return h, nil
	}
	rkey := site.Name + "\x00" + root
	res := d.resolvers[rkey]
	if res == nil {
		if d.failed[rkey] {
			return nil, fspath.ErrNotFound
		}
		var err error
		res, err = fspath.New(root, fspath.Policy{
			Symlinks:      symlinkPolicy(site.Symlinks),
			AllowDotfiles: site.Dotfiles == model.DotfilesAllow,
		})
		if err != nil {
			d.failed[rkey] = true
			return nil, err
		}
		d.resolvers[rkey] = res
	}
	h := staticfiles.New(res, staticfiles.Options{Index: index})
	d.handlers[key] = h
	return h, nil
}

// Close releases the opened document roots.
func (d *DiskFiles) Close() {
	for _, r := range d.resolvers {
		r.Close()
	}
}

func symlinkPolicy(p string) fspath.SymlinkPolicy {
	switch p {
	case model.SymlinksFollow:
		return fspath.SymlinksFollow
	case model.SymlinksOwnerMatch:
		return fspath.SymlinksOwnerMatch
	}
	return fspath.SymlinksDeny
}

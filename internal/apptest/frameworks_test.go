package apptest

import (
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/laikait/lotus/internal/sitefile"
)

// composerCmd is the Composer command, or the test is skipped.
func composerCmd(t *testing.T) string {
	t.Helper()
	if c := os.Getenv("LOTUS_COMPOSER"); c != "" {
		return c
	}
	p, err := exec.LookPath("composer")
	if err != nil {
		t.Skip("composer not found (set LOTUS_COMPOSER)")
	}
	return p
}

// composer runs Composer in dir with its home and cache inside the test cache.
func (h *harness) composer(dir string, args ...string) {
	h.t.Helper()
	cmd := exec.Command(composerCmd(h.t), args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "COMPOSER_HOME="+filepath.Join(h.cache, "composer-home"), "COMPOSER_CACHE_DIR="+filepath.Join(h.cache, "composer-cache"), "COMPOSER_ALLOW_SUPERUSER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Skipf("composer %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// ---- Symfony ----

const symfonyController = `<?php
namespace App\Controller;

use Symfony\Bundle\FrameworkBundle\Controller\AbstractController;
use Symfony\Component\HttpFoundation\JsonResponse;
use Symfony\Component\HttpFoundation\Request;
use Symfony\Component\HttpFoundation\Response;
use Symfony\Component\Routing\Attribute\Route;

class HelloController extends AbstractController
{
    #[Route('/hello/{name}', name: 'hello')]
    public function hello(string $name): Response
    {
        return new Response('Hello ' . $name);
    }

    #[Route('/info', name: 'info')]
    public function info(Request $r): JsonResponse
    {
        return new JsonResponse(['path' => $r->getPathInfo(), 'base' => $r->getBaseUrl(), 'method' => $r->getMethod(), 'q' => $r->query->get('x')]);
    }

    #[Route('/echo', name: 'echo', methods: ['POST'])]
    public function echo(Request $r): Response
    {
        return new Response('got ' . $r->request->get('a'));
    }
}
`

func TestSymfony(t *testing.T) {
	h := setup(t)
	project := filepath.Join(h.cache, "symfony-project")
	if _, err := os.Stat(filepath.Join(project, "bin", "console")); err != nil {
		t.Log("creating a Symfony project with composer (kept in the cache)")
		h.composer(h.cache, "create-project", "symfony/skeleton", filepath.Base(project), "--no-interaction", "--prefer-dist", "--no-progress")
	}
	if err := os.WriteFile(filepath.Join(project, "src", "Controller", "HelloController.php"), []byte(symfonyController), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(project, "public", "robots.txt"), []byte("User-agent: *\n"), 0o644)
	_ = os.RemoveAll(filepath.Join(project, "var", "cache"))

	base := h.start(sitefile.Spec{Name: "sf.test", Template: "symfony", Root: filepath.ToSlash(project)})

	h.expect(h.get(base+"/hello/lotus"), 200, "Hello lotus", "a route of the application")
	r := h.get(base + "/info?x=1")
	h.expect(r, 200, `"path":"\/info"`, "the request as Symfony sees it")
	if !strings.Contains(r.Body, `"q":"1"`) || !strings.Contains(r.Body, `"base":""`) {
		t.Errorf("query string or base URL wrong: %s", r.Body)
	}
	r = h.get(base + "/index.php/hello/pathinfo")
	h.expect(r, 200, "Hello pathinfo", "PATH_INFO: /index.php/hello/...")
	r = h.get(base + "/no/such/route")
	h.expect(r, 404, "", "an unknown URL goes to Symfony and is a 404")
	h.expect(h.post(base+"/echo", url.Values{"a": {"b"}}), 200, "got b", "a POST")
	h.expect(h.get(base+"/robots.txt"), 200, "User-agent", "a static file of public/")
	// public/ is the root: the project's own files are not reachable.
	for _, p := range []string{"/.env", "/composer.json", "/bin/console", "/src/Controller/HelloController.php", "/var/log/dev.log", "/vendor/autoload.php", "/config/services.yaml"} {
		rr := h.get(base + p)
		if rr.Status == 200 && !strings.Contains(rr.Body, "No route found") {
			t.Errorf("%s was served (%d): %.100s", p, rr.Status, rr.Body)
		}
	}
	// Only index.php may be run: a script someone puts in the uploads folder is refused.
	_ = os.MkdirAll(filepath.Join(project, "public", "uploads"), 0o755)
	_ = os.WriteFile(filepath.Join(project, "public", "uploads", "x.php"), []byte("<?php echo 'RAN';"), 0o644)
	if rr := h.get(base + "/uploads/x.php"); rr.Status == 200 || strings.Contains(rr.Body, "RAN") {
		t.Errorf("a PHP file in uploads ran: %d %.100s", rr.Status, rr.Body)
	}
	req, _ := http.NewRequest("HEAD", base+"/hello/x", nil)
	if rr := h.do(req); rr.Status != 200 {
		t.Errorf("HEAD: %d", rr.Status)
	}
}

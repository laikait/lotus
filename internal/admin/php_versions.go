package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/laikait/lotus/internal/phpinstall"
	"github.com/laikait/lotus/internal/sitefile"
)

// PHPInstaller is what the PHP Versions page needs; *phpinstall.Installer is the real one, tests use a fake.
type PHPInstaller interface {
	Supported() bool
	Kind() string // "zip" (Windows builds) or "packages" (Linux)
	CanInstall() bool
	ExtensionsEditable() bool
	CheckRuntime() error
	Releases(ctx context.Context) ([]phpinstall.Release, error)
	List() ([]phpinstall.Installed, error)
	Get(version string) (phpinstall.Installed, error)
	Install(ctx context.Context, version string, progress phpinstall.Progress) error
	Remove(version string) error
	IniState(version string) (phpinstall.IniState, error)
	ApplyIni(version string, settings map[string]string, extensions map[string]bool) error
	IonCube(version string) (phpinstall.IonCubeState, error)
	InstallIonCube(ctx context.Context, version string, progress phpinstall.Progress) error
	SetIonCube(version string, on bool) error
}

const (
	installTimeout = 15 * time.Minute
	jobKeepTime    = 10 * time.Minute
)

// installJob is one background install, polled by the page.
type installJob struct {
	ID       string    `json:"id"`
	Version  string    `json:"version"`
	Label    string    `json:"label"`
	State    string    `json:"state"` // running, done, failed
	Percent  int       `json:"percent"`
	Message  string    `json:"message"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"-"`
}

func (s *Server) phpInstallerOK(w http.ResponseWriter) bool {
	if s.opts.PHPInstall == nil {
		writeError(w, http.StatusNotFound, "PHP versions are not available")
		return false
	}
	return true
}

// poolsUsing lists the php_backend names of the configuration whose program is inside the version's folder.
func (s *Server) poolsUsing(v phpinstall.Installed) []string {
	var names []string
	for name, cmd := range s.opts.Sites.PHPBackendCommands() {
		if commandUses(cmd, v) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// commandUses says whether a pool's program is this version's php-cgi, or lies inside its folder.
func commandUses(cmd string, v phpinstall.Installed) bool {
	c := strings.ToLower(filepath.ToSlash(cmd))
	return c == strings.ToLower(filepath.ToSlash(v.Exe)) || strings.HasPrefix(c, strings.ToLower(filepath.ToSlash(v.Dir))+"/")
}

type installedView struct {
	phpinstall.Installed
	Pools []string `json:"pools"`
}

type availableView struct {
	Branch    string `json:"branch"`
	Version   string `json:"version"`
	Size      int64  `json:"size"`
	Installed bool   `json:"installed"`
	Found     bool   `json:"found"` // already on the system: "use" it instead of installing
}

func (s *Server) phpVersions(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	in := s.opts.PHPInstall
	out := map[string]any{"kind": in.Kind(), "can_install": in.CanInstall(), "supported": in.Supported(), "runtime_ok": true, "runtime_message": "", "feed_error": "", "job": s.currentJob(), "default_pool": s.defaultPool()}
	if !in.Supported() {
		out["installed"], out["available"] = []installedView{}, []availableView{}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err := in.CheckRuntime(); err != nil {
		out["runtime_ok"], out["runtime_message"] = false, err.Error()
	}
	list, err := in.List()
	if err != nil {
		s.log.Error("listing PHP versions failed", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the PHP folder")
		return
	}
	have := map[string]bool{}
	inst := []installedView{}
	for _, v := range list {
		have[v.Version] = true
		pools := s.poolsUsing(v)
		if pools == nil {
			pools = []string{}
		}
		inst = append(inst, installedView{Installed: v, Pools: pools})
	}
	avail := []availableView{}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if rel, err := in.Releases(ctx); err != nil {
		out["feed_error"] = err.Error()
	} else {
		for _, x := range rel {
			avail = append(avail, availableView{Branch: x.Branch, Version: x.Version, Size: x.Size, Installed: have[x.Version], Found: x.Found != ""})
		}
	}
	out["installed"], out["available"] = inst, avail
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) currentJob() *installJob {
	s.jobMu.Lock()
	defer s.jobMu.Unlock()
	var latest *installJob
	for id, j := range s.jobs {
		if j.State != "running" && s.opts.Now().Sub(j.Finished) > jobKeepTime {
			delete(s.jobs, id)
			continue
		}
		if latest == nil || j.Started.After(latest.Started) {
			c := *j
			latest = &c
		}
	}
	return latest
}

type installBody struct {
	Version string `json:"version"`
}

func (s *Server) phpInstall(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	var body installBody
	if !decodeBody(w, r, &body) {
		return
	}
	if !phpinstall.ValidVersion(body.Version) {
		writeError(w, http.StatusUnprocessableEntity, "choose a version from the list")
		return
	}
	in := s.opts.PHPInstall
	if !in.Supported() {
		writeError(w, http.StatusUnprocessableEntity, phpinstall.ErrUnsupported.Error())
		return
	}
	if err := in.CheckRuntime(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	version := body.Version
	s.startJob(w, r, sess, version, "Installing PHP "+version, "PHP "+version+" is installed", func(ctx context.Context, p phpinstall.Progress) error {
		return in.Install(ctx, version, p)
	})
}

// startJob runs a long operation in the background as the one install job, and answers 202 with the job's id.
func (s *Server) startJob(w http.ResponseWriter, r *http.Request, sess *session, version, label, doneText string, run func(context.Context, phpinstall.Progress) error) {
	s.jobMu.Lock()
	for _, j := range s.jobs {
		if j.State == "running" {
			s.jobMu.Unlock()
			writeError(w, http.StatusConflict, "another PHP install is running")
			return
		}
	}
	if s.jobs == nil {
		s.jobs = map[string]*installJob{}
	}
	job := &installJob{ID: token()[:16], Version: version, Label: label, State: "running", Percent: 0, Message: "Starting", Started: s.opts.Now()}
	s.jobs[job.ID] = job
	s.jobMu.Unlock()

	s.log.Info("php job started", "job", label, "user", sess.user, "ip", remote(r))
	go s.runJob(job, sess.user, doneText, run)
	writeJSON(w, http.StatusAccepted, map[string]any{"job": job.ID})
}

func (s *Server) runJob(job *installJob, user, doneText string, run func(context.Context, phpinstall.Progress) error) {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	err := run(ctx, func(pct int, msg string) {
		s.jobMu.Lock()
		job.Percent, job.Message = pct, msg
		s.jobMu.Unlock()
	})
	s.jobMu.Lock()
	job.Finished = s.opts.Now()
	if err != nil {
		job.State, job.Message = "failed", err.Error()
	} else {
		job.State, job.Percent, job.Message = "done", 100, doneText
	}
	s.jobMu.Unlock()
	if err != nil {
		s.log.Warn("php job failed", "job", job.Label, "user", user, "error", err)
		return
	}
	s.log.Info("php job done", "job", job.Label, "user", user)
}

func (s *Server) phpJob(w http.ResponseWriter, r *http.Request, _ *session) {
	id := r.PathValue("id")
	s.jobMu.Lock()
	var out *installJob
	if j := s.jobs[id]; j != nil {
		c := *j
		out = &c
	}
	s.jobMu.Unlock()
	if out == nil {
		writeError(w, http.StatusNotFound, "there is no such install")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) phpRemove(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	version := r.PathValue("version")
	v, err := s.opts.PHPInstall.Get(version)
	if err != nil {
		s.phpFailure(w, err)
		return
	}
	if pools := s.poolsUsing(v); len(pools) > 0 {
		writeError(w, http.StatusConflict, "PHP "+version+" is used by the pool "+strings.Join(pools, ", ")+"; delete the pool first")
		return
	}
	if err := s.opts.PHPInstall.Remove(version); err != nil {
		s.phpFailure(w, err)
		return
	}
	s.log.Info("php removed", "version", version, "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) phpIniGet(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	st, err := s.opts.PHPInstall.IniState(r.PathValue("version"))
	if err != nil {
		s.phpFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": st.Settings, "enabled": st.Enabled, "available": st.Available, "setting_info": phpinstall.Settings, "extensions_editable": s.opts.PHPInstall.ExtensionsEditable()})
}

type iniBody struct {
	Settings   map[string]string `json:"settings"`
	Extensions map[string]bool   `json:"extensions"`
}

func (s *Server) phpIniPut(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	version := r.PathValue("version")
	var body iniBody
	if !decodeBody(w, r, &body) {
		return
	}
	v, err := s.opts.PHPInstall.Get(version)
	if err != nil {
		s.phpFailure(w, err)
		return
	}
	if err := s.opts.PHPInstall.ApplyIni(version, body.Settings, body.Extensions); err != nil {
		s.phpFailure(w, err)
		return
	}
	// New processes read php.ini when they start, so replace the ones of the pools that use this version.
	restarted := []string{}
	if s.opts.PHPRestart != nil {
		for _, name := range s.poolsUsing(v) {
			if s.opts.PHPRestart(name) {
				restarted = append(restarted, name)
			}
		}
	}
	s.log.Info("php.ini changed", "version", version, "settings", len(body.Settings), "extensions", len(body.Extensions), "pools_restarted", len(restarted), "user", sess.user, "ip", remote(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarted": restarted})
}

type poolBody struct {
	Name      string `json:"name"`
	Processes int    `json:"processes"`
}

// phpMakePool adds a pool for an installed version through the server settings file, like the PHP page's pool form.
func (s *Server) phpMakePool(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	version := r.PathValue("version")
	var body poolBody
	if !decodeBody(w, r, &body) {
		return
	}
	v, err := s.opts.PHPInstall.Get(version)
	if err != nil {
		s.phpFailure(w, err)
		return
	}
	sp, ok := s.serverSpec(w)
	if !ok {
		return
	}
	taken := map[string]bool{}
	for _, p := range sp.PHP {
		taken[p.Name] = true
	}
	for name := range s.opts.Sites.PHPBackendCommands() {
		taken[name] = true
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = defaultPoolName(version, taken)
	}
	if taken[name] {
		writeError(w, http.StatusConflict, fmt.Sprintf("a pool named %q exists; choose another name", name))
		return
	}
	if body.Processes == 0 {
		body.Processes = 4
	}
	sp = sp.AddPool(sitefile.PHPPoolSpec{
		Name: name, Command: filepath.ToSlash(v.Exe), Processes: body.Processes, MaxRequests: 500, PHPIni: filepath.ToSlash(v.Ini), Timeout: 60,
	})
	s.saveServer(w, r, sess, "php pool made for "+version, name, sp, false)
}

// defaultPoolName is php83 for 8.3.x, or php83-37 when php83 is taken (two patch versions side by side).
func defaultPoolName(version string, taken map[string]bool) string {
	parts := strings.Split(version, ".")
	name := "php" + parts[0] + parts[1]
	if taken[name] {
		name += "-" + parts[2]
	}
	return name
}

func (s *Server) phpFailure(w http.ResponseWriter, err error) {
	var ve *phpinstall.ValueError
	switch {
	case errors.Is(err, phpinstall.ErrNotInstalled):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.As(err, &ve):
		writeError(w, http.StatusUnprocessableEntity, ve.Msg)
	default:
		s.log.Error("php versions request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "the request failed (see the server log)")
	}
}

// phpLabels names each php_backend by the PHP version it runs, for the site form's dropdown: "PHP 8.3.37 (php83)".
// A backend that is not one of the installed versions (a path someone wrote, or FastCGI) has no entry.
func (s *Server) phpLabels() map[string]string {
	out := map[string]string{}
	if s.opts.PHPInstall == nil || s.opts.Sites == nil {
		return out
	}
	list, err := s.opts.PHPInstall.List()
	if err != nil {
		return out
	}
	for name, cmd := range s.opts.Sites.PHPBackendCommands() {
		for _, v := range list {
			if commandUses(cmd, v) {
				out[name] = "PHP " + v.Version + " (" + name + ")"
			}
		}
	}
	return out
}

// ---- ionCube Loader ----

func (s *Server) phpIonCubeGet(w http.ResponseWriter, r *http.Request, _ *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	st, err := s.opts.PHPInstall.IonCube(r.PathValue("version"))
	if err != nil {
		s.phpFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

type ionBody struct {
	Action string `json:"action"` // install, enable, disable
}

func (s *Server) phpIonCubePost(w http.ResponseWriter, r *http.Request, sess *session) {
	if !s.phpInstallerOK(w) {
		return
	}
	version := r.PathValue("version")
	var body ionBody
	if !decodeBody(w, r, &body) {
		return
	}
	in := s.opts.PHPInstall
	v, err := in.Get(version)
	if err != nil {
		s.phpFailure(w, err)
		return
	}
	switch body.Action {
	case "install":
		s.startJob(w, r, sess, version, "Installing the ionCube Loader for PHP "+version, "The ionCube Loader is enabled for PHP "+version, func(ctx context.Context, p phpinstall.Progress) error {
			if err := in.InstallIonCube(ctx, version, p); err != nil {
				return err
			}
			s.recycle(v)
			return nil
		})
	case "enable", "disable":
		if err := in.SetIonCube(version, body.Action == "enable"); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		restarted := s.recycle(v)
		s.log.Info("ioncube changed", "version", version, "action", body.Action, "pools_restarted", len(restarted), "user", sess.user, "ip", remote(r))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "restarted": restarted})
	default:
		writeError(w, http.StatusUnprocessableEntity, "the action must be install, enable or disable")
	}
}

// recycle replaces the processes of the pools that use a version, so they read the new php.ini.
func (s *Server) recycle(v phpinstall.Installed) []string {
	restarted := []string{}
	if s.opts.PHPRestart != nil {
		for _, name := range s.poolsUsing(v) {
			if s.opts.PHPRestart(name) {
				restarted = append(restarted, name)
			}
		}
	}
	return restarted
}

// defaultPool is the pool new PHP sites start with ("" when there is no pool).
func (s *Server) defaultPool() string {
	if s.opts.Sites == nil {
		return ""
	}
	sp, _, err := s.opts.Sites.Server()
	if err != nil {
		return ""
	}
	return sp.DefaultPool()
}

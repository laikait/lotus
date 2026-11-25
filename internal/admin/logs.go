package admin

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// LogSource is a log file the admin may show. The admin never opens a path a client sends: the page names a source by
// its id, and the server looks the path up here.
type LogSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"-"`
}

const (
	defaultLogLines = 200
	maxLogLines     = 1000
	logTailBytes    = 512 << 10 // the end of the file that is searched
	maxLogLineLen   = 2000
	maxFilterLen    = 100
)

func (s *Server) logSources() []LogSource {
	if s.opts.Logs == nil {
		return nil
	}
	return s.opts.Logs()
}

func (s *Server) logList(w http.ResponseWriter, r *http.Request, _ *session) {
	list := s.logSources()
	if list == nil {
		list = []LogSource{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) logTail(w http.ResponseWriter, r *http.Request, _ *session) {
	id := r.PathValue("id")
	var src *LogSource
	for _, c := range s.logSources() {
		if c.ID == id {
			c := c
			src = &c
			break
		}
	}
	if src == nil {
		writeError(w, http.StatusNotFound, "there is no such log")
		return
	}
	n := defaultLogLines
	if v := r.URL.Query().Get("lines"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 {
			n = min(k, maxLogLines)
		}
	}
	filter := strings.ToLower(r.URL.Query().Get("filter"))
	if len(filter) > maxFilterLen {
		writeError(w, http.StatusUnprocessableEntity, "the filter is too long")
		return
	}
	lines, size, err := tailFile(src.Path, n, filter)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusOK, map[string]any{"lines": []string{}, "size": 0, "note": "The file does not exist yet."})
			return
		}
		s.log.Error("reading a log failed", "log", id, "error", err)
		writeError(w, http.StatusInternalServerError, "the log could not be read (see the server log)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "size": size})
}

// tailFile returns the last n lines of the end of the file that contain filter (lower case; empty: all), and the file's
// size. Only the last logTailBytes are read, and a line cut by that start is dropped.
func tailFile(path string, n int, filter string) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := fi.Size()
	start := int64(0)
	if size > logTailBytes {
		start = size - logTailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, 0, err
	}
	rd := bufio.NewReaderSize(f, 64<<10)
	if start > 0 {
		_, _ = rd.ReadString('\n') // the first line is probably cut
	}
	ring := make([]string, 0, n)
	for {
		line, err := rd.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			if len(line) > maxLogLineLen {
				line = line[:maxLogLineLen] + " ..."
			}
			if filter == "" || strings.Contains(strings.ToLower(line), filter) {
				if len(ring) == n {
					copy(ring, ring[1:])
					ring[n-1] = line
				} else {
					ring = append(ring, line)
				}
			}
		}
		if err != nil {
			break
		}
	}
	return ring, size, nil
}

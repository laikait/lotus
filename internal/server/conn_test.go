package server

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// readerFromWriter is a ResponseWriter that offers io.ReaderFrom, like the real
// one does when it can use sendfile.
type readerFromWriter struct {
	*httptest.ResponseRecorder
	readFromCalls int
}

func (w *readerFromWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFromCalls++
	return io.Copy(w.ResponseRecorder, src)
}

func TestRecorderKeepsSendfilePathAndCountsBytes(t *testing.T) {
	under := &readerFromWriter{ResponseRecorder: httptest.NewRecorder()}
	rec := &recorder{ResponseWriter: under}

	var w http.ResponseWriter = rec
	rf, ok := w.(io.ReaderFrom)
	if !ok {
		t.Fatal("the recorder must implement io.ReaderFrom, or file responses lose sendfile")
	}
	n, err := rf.ReadFrom(strings.NewReader(strings.Repeat("x", 4096)))
	if err != nil || n != 4096 {
		t.Fatalf("ReadFrom = %d, %v", n, err)
	}
	if under.readFromCalls != 1 {
		t.Errorf("the underlying ReadFrom was called %d times, want 1 (zero-copy path)", under.readFromCalls)
	}
	if rec.bytes != 4096 || rec.statusCode() != http.StatusOK {
		t.Errorf("bytes=%d status=%d", rec.bytes, rec.statusCode())
	}
}

func TestRecorderFallsBackWhenTheWriterHasNoReadFrom(t *testing.T) {
	// httptest.ResponseRecorder has no ReadFrom: the copy must still work, and
	// must not recurse into the recorder's own ReadFrom.
	under := httptest.NewRecorder()
	rec := &recorder{ResponseWriter: under}
	n, err := rec.ReadFrom(bytes.NewReader([]byte("hello")))
	if err != nil || n != 5 || under.Body.String() != "hello" || rec.bytes != 5 {
		t.Errorf("n=%d err=%v body=%q bytes=%d", n, err, under.Body.String(), rec.bytes)
	}
}

func TestRecorderStatus(t *testing.T) {
	rec := &recorder{ResponseWriter: httptest.NewRecorder()}
	if rec.statusCode() != 200 {
		t.Error("a handler that writes nothing is a 200")
	}
	rec.WriteHeader(404)
	rec.WriteHeader(500) // net/http ignores the second call; so must the log
	rec.Write([]byte("abc"))
	if rec.statusCode() != 404 || rec.bytes != 3 {
		t.Errorf("status=%d bytes=%d", rec.statusCode(), rec.bytes)
	}
	if rec.Unwrap() == nil {
		t.Error("Unwrap must expose the real writer (flush, deadlines, hijack)")
	}
}

func TestLimitConnReleasesItsSlotOnce(t *testing.T) {
	lim := newConnLimiter(1)
	srv, cli := net.Pipe()
	defer cli.Close()
	lim.slots <- struct{}{}
	c := &limitConn{Conn: srv, release: func() { <-lim.slots }}

	c.Close()
	c.Close() // closing twice must not release a second slot
	c.Close()

	select {
	case lim.slots <- struct{}{}:
	default:
		t.Fatal("the slot was not released")
	}
	select {
	case lim.slots <- struct{}{}:
		t.Fatal("more slots were released than were taken")
	default:
	}
}

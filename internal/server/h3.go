package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/laikait/lotus/internal/config/model"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// HTTP/3 (docs/architecture/http3.md).
//
// A listener with `http3 on` also serves HTTP/3 over UDP on the same port
// number. The requests go to the same handler as HTTP/1.1 and HTTP/2 ones, so
// sites, limits, .htaccess, the proxy and access logs behave the same; this
// file only deals with the QUIC connection: binding, connection limits,
// timeouts, advertising and shutdown.

// altSvcMaxAge is how long browsers remember that HTTP/3 is available.
const altSvcMaxAge = 24 * time.Hour

// h3Listener is the HTTP/3 side of a listener.
type h3Listener struct {
	srv *http3.Server
	ln  *limitedQUIC
	udp net.PacketConn
	// port is the UDP port, which Alt-Svc announces.
	port int
}

// limitedQUIC accepts QUIC connections and holds each to the same connection
// limits as the TCP ones: the server-wide maximum and the per-address maximum.
type limitedQUIC struct {
	*quic.EarlyListener
	lim *connLimiter
}

func (l *limitedQUIC) Accept(ctx context.Context) (*quic.Conn, error) {
	for {
		conn, err := l.EarlyListener.Accept(ctx)
		if err != nil {
			return nil, err
		}
		release, ok := l.lim.acquire(conn.RemoteAddr())
		if !ok {
			_ = conn.CloseWithError(quic.ApplicationErrorCode(http3.ErrCodeExcessiveLoad), "too many connections")
			continue
		}
		go func() {
			<-conn.Context().Done()
			release()
		}()
		return conn, nil
	}
}

// listenTCPAndUDP binds the TCP listener of an address and the UDP port with
// the same number. When the port is 0 the system picks one; if that number is
// taken for UDP (or reserved: Windows does this for ranges) another is tried.
func listenTCPAndUDP(address string) (net.Listener, net.PacketConn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, nil, err
	}
	attempts := 1
	if port == "0" {
		attempts = 25
	}
	var last error
	for i := 0; i < attempts; i++ {
		ln, err := net.Listen("tcp", address)
		if err != nil {
			return nil, nil, err
		}
		udp, err := net.ListenPacket("udp", net.JoinHostPort(host, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)))
		if err == nil {
			return ln, udp, nil
		}
		ln.Close()
		last = err
	}
	return nil, nil, fmt.Errorf("http3 (UDP): %w", last)
}

// startH3 binds the UDP port of a listener with http3 and starts serving. The
// TCP listener is already bound, so a port chosen by the system (":0") is the
// same for both.
func (s *Server) startH3(bl *boundListener, limits model.Limits) error {
	udp := bl.udp
	bl.udp = nil
	srv := &http3.Server{
		Handler:           bl.srv.Handler,
		MaxHeaderBytes:    int(limits.MaxHeaderBytes),
		ReadHeaderTimeout: time.Duration(limits.HeaderTimeout),
		IdleTimeout:       time.Duration(limits.IdleTimeout),
		Logger:            s.log.With("listener", bl.conf.Name, "protocol", "http3"),
	}
	qc := &quic.Config{
		// 0-RTT data can be replayed by an attacker, and requests aren't
		// necessarily safe to repeat: never accepted.
		Allow0RTT:          false,
		MaxIncomingStreams: 100,
		MaxIdleTimeout:     time.Duration(limits.IdleTimeout),
	}
	ln, err := quic.ListenEarly(udp, http3.ConfigureTLSConfig(bl.srv.TLSConfig.Clone()), qc)
	if err != nil {
		udp.Close()
		return fmt.Errorf("http3: %w", err)
	}
	h := &h3Listener{srv: srv, ln: &limitedQUIC{EarlyListener: ln, lim: s.limiter}, udp: udp, port: udp.LocalAddr().(*net.UDPAddr).Port}
	bl.h3 = h
	alt := fmt.Sprintf(`h3=":%d"; ma=%d`, h.port, int(altSvcMaxAge/time.Second))
	bl.altSvc.Store(&alt)
	go func() {
		if err := srv.ServeListener(h.ln); err != nil && err != http.ErrServerClosed {
			s.errc <- fmt.Errorf("listener %q (HTTP/3): %w", bl.conf.Name, err)
		}
	}()
	return nil
}

// stop shuts the HTTP/3 side down: running requests get until ctx ends.
func (h *h3Listener) stop(ctx context.Context) error {
	err := h.srv.Shutdown(ctx)
	if err != nil {
		h.srv.Close()
	}
	h.ln.Close()
	h.udp.Close()
	return err
}

// h3Deadlines applies the server's read and write timeouts to one HTTP/3
// request, as net/http does for the TCP ones.
func h3Deadlines(w http.ResponseWriter, l model.Limits) {
	rc := http.NewResponseController(w)
	now := time.Now()
	if l.ReadTimeout > 0 {
		_ = rc.SetReadDeadline(now.Add(time.Duration(l.ReadTimeout)))
	}
	if l.WriteTimeout > 0 {
		_ = rc.SetWriteDeadline(now.Add(time.Duration(l.WriteTimeout)))
	}
}

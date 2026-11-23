// Package fastcgi is a client for the FastCGI protocol (the "responder" role),
// enough to run PHP through PHP-FPM or php-cgi.
//
// Requests are not multiplexed: each connection carries one request at a time,
// and finished connections go back to a small pool.
package fastcgi

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Record types and constants from the FastCGI specification, version 1.
const (
	version1 = 1

	typeBeginRequest uint8 = 1
	typeEndRequest   uint8 = 3
	typeParams       uint8 = 4
	typeStdin        uint8 = 5
	typeStdout       uint8 = 6
	typeStderr       uint8 = 7

	roleResponder uint16 = 1
	flagKeepConn  uint8  = 1

	statusComplete   uint8 = 0
	statusCantMPX    uint8 = 1
	statusOverloaded uint8 = 2
	statusUnknownRol uint8 = 3

	// requestID is the only ID used: requests are not multiplexed.
	requestID uint16 = 1

	headerLen = 8
	// maxContent is the most a record can carry. Records are written with at
	// most maxChunk bytes so that content plus padding stays a multiple of 8.
	maxContent = 65535
	maxChunk   = 65528
)

var zeros [8]byte

// writeRecord writes one record. content must be at most maxContent bytes.
func writeRecord(w *bufio.Writer, typ uint8, content []byte) error {
	var h [headerLen]byte
	h[0] = version1
	h[1] = typ
	binary.BigEndian.PutUint16(h[2:], requestID)
	binary.BigEndian.PutUint16(h[4:], uint16(len(content)))
	pad := (8 - len(content)%8) % 8
	h[6] = byte(pad)
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	if _, err := w.Write(content); err != nil {
		return err
	}
	_, err := w.Write(zeros[:pad])
	return err
}

// writeStream writes data as a sequence of records of one type. It does not
// write the empty record that ends a stream.
func writeStream(w *bufio.Writer, typ uint8, data []byte) error {
	for len(data) > 0 {
		n := len(data)
		if n > maxChunk {
			n = maxChunk
		}
		if err := writeRecord(w, typ, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// appendLength appends a name or value length: one byte below 128, otherwise
// four bytes with the top bit set.
func appendLength(b []byte, n int) []byte {
	if n < 128 {
		return append(b, byte(n))
	}
	return append(b, byte(n>>24)|0x80, byte(n>>16), byte(n>>8), byte(n))
}

// encodeParams encodes name-value pairs for a PARAMS stream.
func encodeParams(params []Param) []byte {
	size := 0
	for _, p := range params {
		size += 8 + len(p.Name) + len(p.Value)
	}
	b := make([]byte, 0, size)
	for _, p := range params {
		b = appendLength(b, len(p.Name))
		b = appendLength(b, len(p.Value))
		b = append(b, p.Name...)
		b = append(b, p.Value...)
	}
	return b
}

// errProtocol marks a malformed record or an impossible sequence of records.
var errProtocol = errors.New("fastcgi: protocol error")

func protocolError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errProtocol, fmt.Sprintf(format, args...))
}

// readRecord reads one record. The returned content is valid until the next
// call (it lives in *buf, which is grown as needed).
func readRecord(r *bufio.Reader, buf *[]byte) (typ uint8, id uint16, content []byte, err error) {
	var h [headerLen]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return 0, 0, nil, err
	}
	if h[0] != version1 {
		return 0, 0, nil, protocolError("unsupported version %d", h[0])
	}
	typ = h[1]
	id = binary.BigEndian.Uint16(h[2:])
	n := int(binary.BigEndian.Uint16(h[4:]))
	total := n + int(h[6])
	if cap(*buf) < total {
		*buf = make([]byte, maxContent+255)
	}
	b := (*buf)[:total]
	if _, err = io.ReadFull(r, b); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, 0, nil, err
	}
	return typ, id, b[:n], nil
}

package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrUnreachable means no server answered on the socket.
var ErrUnreachable = errors.New("the lotus control socket is unreachable")

// Client is one connection to a server's control channel.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	nextID int
	Hello  HelloResult
	// Timeout bounds each call. Zero means no limit.
	Timeout time.Duration
}

// Dial connects to the socket at path and performs the handshake.
func Dial(path, clientName string) (*Client, error) {
	conn, err := net.DialTimeout("unix", path, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): %v", ErrUnreachable, path, err)
	}
	return NewClient(conn, clientName)
}

// NewClient performs the handshake on an existing connection.
func NewClient(conn net.Conn, clientName string) (*Client, error) {
	c := &Client{conn: conn, r: bufio.NewReaderSize(conn, 4096), Timeout: 30 * time.Second}
	if err := c.Call(MethodHello, HelloParams{Protocol: Protocol, Client: clientName}, &c.Hello); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Call sends one request and decodes the result into result (which may be
// nil). A failed call returns the server's *Error.
func (c *Client) Call(method string, params, result any) error {
	c.nextID++
	req := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{jsonRPCVersion, c.nextID, method, params}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if c.Timeout > 0 {
		_ = c.conn.SetDeadline(time.Now().Add(c.Timeout))
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	line, err := readLine(c.r, MaxMessageSize*4)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("bad reply from the server: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if resp.ID != c.nextID {
		return fmt.Errorf("bad reply from the server: id %d, want %d", resp.ID, c.nextID)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("bad reply from the server: %w", err)
		}
	}
	return nil
}

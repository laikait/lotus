// Package control implements the local control channel
// (docs/architecture/control-protocol.md): how the lotus command talks to a
// running server. It is JSON-RPC 2.0 over a local socket, one message per
// line. The package knows nothing about the server itself; the application
// supplies a Backend.
package control

import (
	"encoding/json"
	"fmt"
	"time"
)

// Protocol is the protocol version spoken on the channel.
const Protocol = "lotus.control.v1"

// Limits (control-protocol.md §2).
const (
	MaxMessageSize = 1 << 20
	IdleTimeout    = 60 * time.Second
	MaxConnections = 16
)

// Error codes (control-protocol.md §5).
const (
	CodeParse            = -32700
	CodeInvalidRequest   = -32600
	CodeMethodNotFound   = -32601
	CodeInvalidParams    = -32602
	CodePermissionDenied = -32001
	CodeUnsupportedProto = -32010
	CodeConfigInvalid    = -32020
	CodeReloadInProgress = -32021
	CodeNotSupported     = -32030
	CodeNotFound         = -32040
	CodeInternal         = -32050
	jsonRPCVersion       = "2.0"
	MethodStatus         = "lotus.status"
	MethodHealth         = "lotus.health"
	MethodReload         = "lotus.reload"
	MethodStop           = "lotus.stop"
	MethodLogsReopen     = "lotus.logs.reopen"
	MethodConfigInspect  = "lotus.config.inspect"
	MethodSiteList       = "lotus.site.list"
	MethodHello          = "lotus.hello"
	roleOperator         = "operator"
)

// Error is a JSON-RPC error. It is also what the client returns for a failed call.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// NewError builds an Error, with optional data that is marshalled to JSON.
func NewError(code int, message string, data any) *Error {
	e := &Error{Code: code, Message: message}
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			e.Data = b
		}
	}
	return e
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// HelloParams is the client's first call.
type HelloParams struct {
	Protocol string `json:"protocol"`
	Client   string `json:"client"`
}

// HelloResult answers the handshake.
type HelloResult struct {
	Protocol string   `json:"protocol"`
	Server   string   `json:"server"`
	PID      int      `json:"pid"`
	Role     string   `json:"role"`
	Methods  []string `json:"methods"`
}

// Listener describes one listener in a status reply.
type Listener struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	HTTPS   bool   `json:"https"`
}

// Status is the result of lotus.status.
type Status struct {
	Version     string     `json:"version"`
	PID         int        `json:"pid"`
	Started     time.Time  `json:"started"`
	Uptime      string     `json:"uptime"`
	Generation  uint64     `json:"generation"`
	ConfigFile  string     `json:"config_file"`
	Listeners   []Listener `json:"listeners"`
	Sites       int        `json:"sites"`
	Goroutines  int        `json:"goroutines"`
	MemoryBytes uint64     `json:"memory_bytes"`
	Rejected    uint64     `json:"rejected_connections"`
}

// Site is one entry of lotus.site.list.
type Site struct {
	Name      string   `json:"name"`
	Aliases   []string `json:"aliases"`
	Listeners []string `json:"listeners"`
	TLS       string   `json:"tls"`
}

// ReloadParams are the parameters of lotus.reload.
type ReloadParams struct {
	Config string `json:"config,omitempty"` // a path; empty means the file the server started with
}

// ReloadResult is the result of a successful reload.
type ReloadResult struct {
	Generation  uint64            `json:"generation"`
	Diagnostics []json.RawMessage `json:"diagnostics"` // warnings, in the format of `config validate --json`
}

// StopParams are the parameters of lotus.stop.
type StopParams struct {
	Graceful *bool  `json:"graceful,omitempty"` // default true
	Timeout  string `json:"timeout,omitempty"`  // a duration such as "30s"
}

// InspectParams are the parameters of lotus.config.inspect.
type InspectParams struct {
	Site string `json:"site,omitempty"`
}

// Health is the result of lotus.health.
type Health struct {
	Status string        `json:"status"` // "ok" or "degraded"
	Checks []HealthCheck `json:"checks"`
}

// HealthCheck is one check in a health result.
type HealthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

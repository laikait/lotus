// Package version reports build information for the lotus binary.
//
// Version, Commit and Date are set at build time with -ldflags, for example:
//
//	go build -ldflags "-X github.com/laikait/lotus/pkg/version.Version=0.1.0"
package version

import (
	"fmt"
	"runtime"
)

// Build metadata, overridden at link time.
var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// ConfigLanguageVersion is the newest lotus_config_version this build understands.
const ConfigLanguageVersion = 1

// ControlProtocol is the control channel protocol version this build speaks.
const ControlProtocol = "lotus.control.v1"

// Info is the machine-readable form of the build information.
type Info struct {
	Version         string `json:"version"`
	Commit          string `json:"commit"`
	Date            string `json:"date"`
	GoVersion       string `json:"go_version"`
	Platform        string `json:"platform"`
	ConfigLanguage  int    `json:"config_language_version"`
	ControlProtocol string `json:"control_protocol"`
}

// Get returns the build information for the running binary.
func Get() Info {
	return Info{
		Version:         Version,
		Commit:          Commit,
		Date:            Date,
		GoVersion:       runtime.Version(),
		Platform:        runtime.GOOS + "/" + runtime.GOARCH,
		ConfigLanguage:  ConfigLanguageVersion,
		ControlProtocol: ControlProtocol,
	}
}

// String returns a one-line human-readable description.
func (i Info) String() string {
	return fmt.Sprintf("lotus %s (commit %s, built %s, %s, %s)",
		i.Version, i.Commit, i.Date, i.GoVersion, i.Platform)
}

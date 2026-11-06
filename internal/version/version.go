// Package version is the K-Vision release number, from the VERSION file next
// to this one. Bump it there; the binary, the app's Info.plist and About all read it.
package version

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var raw string

// Number is e.g. "1.0.0".
var Number = strings.TrimSpace(raw)

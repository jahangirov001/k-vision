// Package window shows the desktop UI in a native window of the app's own: on macOS
// a WebKit (WKWebView) window, so K-Vision has its own name in the menu bar, its
// own Dock icon and its own Cmd+Q -- not a Chrome window. Builds without it (cgo
// off, or other systems for now) fall back to the browser; see Available.
package window

// Options is what the window needs to know.
type Options struct {
	Title   string
	URL     string
	Width   int
	Height  int
	Version string // shown in About
	Tagline string // one line under it in About
}

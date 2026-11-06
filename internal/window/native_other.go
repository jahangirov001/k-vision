//go:build !(darwin && cgo)

package window

// Available reports whether this build has a native window. Without one, `kv ui`
// opens the UI in Chrome or Edge in app mode instead.
func Available() bool { return false }

// Run is never called when Available is false.
func Run(Options) {}

// HasFilePicker is false without the native window; the page uses <input type=file>.
func HasFilePicker() bool { return false }

// ChooseFiles is never called when HasFilePicker is false.
func ChooseFiles(string) []string { return nil }

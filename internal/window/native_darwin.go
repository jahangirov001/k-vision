//go:build darwin && cgo

package window

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#include <stdlib.h>
void kcInstallMenu(const char *appName, const char *version, const char *tagline);
void kcRememberFrame(void *window);
int kcSystemIsDark(void);
void kcWatchAppearance(void);
char *kcChooseFiles(const char *title);
*/
import "C"

import (
	"runtime"
	"strconv"
	"strings"
	"unsafe"

	webview "github.com/webview/webview_go"
)

func init() {
	// Cocoa must run on the process's main thread. Locking here, in init, keeps the
	// main goroutine on it until Run is called from main().
	runtime.LockOSThread()
}

// Available reports whether this build has a native window.
func Available() bool { return true }

// Run opens the window and blocks until it is closed. Must be called from main().
func Run(o Options) {
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(o.Title)
	w.SetSize(o.Width, o.Height, webview.HintNone)
	name, ver, tag := C.CString(o.Title), C.CString(o.Version), C.CString(o.Tagline)
	defer C.free(unsafe.Pointer(name))
	defer C.free(unsafe.Pointer(ver))
	defer C.free(unsafe.Pointer(tag))
	C.kcInstallMenu(name, ver, tag)
	C.kcRememberFrame(w.Window())
	// System mode: push the real system appearance into the page, at load and on
	// every change, rather than relying on the web view's media query alone.
	push := func(dark bool) {
		js := "window.__kcSystemDark=" + map[bool]string{true: "true", false: "false"}[dark] +
			";window.dispatchEvent(new Event('kc-system-theme'))"
		w.Dispatch(func() { w.Eval(js) })
	}
	w.Init("window.__kcSystemDark=" + map[bool]string{true: "true", false: "false"}[C.kcSystemIsDark() == 1] + ";")
	onAppearance = push
	// Menu items that belong to the page call window.kcMenu(name) in it.
	onMenu = func(action string) {
		w.Dispatch(func() { w.Eval("window.kcMenu&&window.kcMenu(" + strconv.Quote(action) + ")") })
	}
	C.kcWatchAppearance()
	w.Navigate(o.URL)
	running = true
	defer func() { running = false }()
	w.Run()
}

var (
	onAppearance func(bool)
	onMenu       func(string)
)

//export kcMenuAction
func kcMenuAction(action *C.char) {
	if onMenu != nil {
		onMenu(C.GoString(action))
	}
}

// ChooseFiles shows the macOS Open panel and returns the chosen files' paths (nil
// when cancelled). Safe to call from any goroutine while the window runs.
func ChooseFiles(title string) []string {
	if !running {
		return nil
	}
	t := C.CString(title)
	defer C.free(unsafe.Pointer(t))
	r := C.kcChooseFiles(t)
	if r == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(r))
	return strings.Split(C.GoString(r), "\n")
}

// HasFilePicker reports whether ChooseFiles can be used (the native window is up).
func HasFilePicker() bool { return running }

var running bool

//export kcAppearanceChanged
func kcAppearanceChanged(dark C.int) {
	if onAppearance != nil {
		onAppearance(dark == 1)
	}
}

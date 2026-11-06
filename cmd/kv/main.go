// Command kv is K-Vision: a terminal UI (default) or the desktop app's server.
// `kv help` prints the full guide.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/njahangirov/kube-console/internal/kube"
	"github.com/njahangirov/kube-console/internal/tui"
	"github.com/njahangirov/kube-console/internal/version"
	"github.com/njahangirov/kube-console/internal/web"
	"github.com/njahangirov/kube-console/internal/window"
)

func main() {
	ctxFlag := flag.String("context", "", "kubeconfig context to start on")
	addr := flag.String("addr", "127.0.0.1:7788", "listen address for `kv ui`")
	noOpen := flag.Bool("no-open", false, "with `kv ui`, print the address instead of opening a window")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Usage = func() { fmt.Print(tui.Guide(width())) }
	flag.Parse()
	if *showVersion {
		fmt.Println("k-vision", version.Number)
		return
	}
	args := flag.Args()
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Print(tui.Guide(width()))
		return
	}
	mgr, err := kube.NewManager()
	if err != nil {
		fail(err)
	}
	if len(args) > 0 && args[0] == "restart" {
		restartApp()
		return
	}
	if len(args) > 0 && args[0] == "ui" {
		runUI(mgr, *addr, !*noOpen)
		return
	}
	kind := "pods"
	if len(args) > 0 {
		kind = args[0]
	}
	if err := tui.Run(mgr, kind, *ctxFlag); err != nil {
		fail(err)
	}
}

// runUI starts the UI server and shows it: in the app's own native window where the
// build has one (macOS), otherwise in a browser window. With the native window,
// closing it quits K-Vision.
func runUI(mgr *kube.Manager, addr string, open bool) {
	// From a terminal, hand over to the installed app: it is the one with the name,
	// icon and Dock entry, and a terminal-started process is none of those. The app
	// itself runs `kv ui` with KV_IN_APP=1, so this does not loop.
	if open && runtime.GOOS == "darwin" && os.Getenv("KV_IN_APP") == "" && os.Getenv("KV_BROWSER") != "1" {
		if exe, err := os.Executable(); err == nil {
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				if i := strings.Index(real, ".app/Contents/MacOS/"); i > 0 {
					if exec.Command("open", real[:i+4]).Run() == nil {
						fmt.Println("Opened K-Vision.")
						return
					}
				}
			}
		}
	}
	if !open || !window.Available() || os.Getenv("KV_BROWSER") == "1" {
		if err := web.Serve(mgr, addr, open); err != nil {
			fail(err)
		}
		return
	}
	in, err := web.Start(mgr, addr)
	if err != nil {
		fail(err)
	}
	if in.Running {
		fmt.Println("K-Vision is already running:", in.Address, "- opening another window on it")
	} else {
		fmt.Println("K-Vision is running at", in.Address)
		go func() {
			if err := in.Serve(); err != nil {
				fail(err)
			}
		}()
		defer in.Close()
	}
	web.FilePicker = window.ChooseFiles
	window.Run(window.Options{Title: "K-Vision", URL: in.URL, Width: 1440, Height: 900,
		Version: version.Number, Tagline: "A console for every Kubernetes cluster in your kubeconfig."})
}

// restartApp is `kv restart`: quit the running K-Vision (the app or a `kv ui`) and
// open it again -- e.g. after an update, or when a cluster's login was refreshed.
func restartApp() {
	pids := runningUI()
	for _, p := range pids {
		if pr, err := os.FindProcess(p); err == nil {
			if runtime.GOOS == "windows" {
				_ = pr.Kill() // Windows has no SIGTERM for console programs
			} else {
				_ = pr.Signal(syscall.SIGTERM)
			}
		}
	}
	// wait for them to go, at most 10 s
	for i := 0; i < 50 && len(runningUI()) > 0; i++ {
		time.Sleep(200 * time.Millisecond)
	}
	if len(pids) == 0 {
		fmt.Println("K-Vision was not running; opening it.")
	} else {
		fmt.Println("Restarting K-Vision…")
	}
	switch runtime.GOOS {
	case "darwin":
		for _, app := range []string{"/Applications/K-Vision.app", filepath.Join(os.Getenv("HOME"), "Applications", "K-Vision.app")} {
			if _, err := os.Stat(app); err == nil {
				if err := exec.Command("open", app).Run(); err == nil {
					fmt.Println("K-Vision is open.")
					return
				}
			}
		}
	}
	// No installed app (Linux, Windows, or a source build): start the window detached.
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	cmd := exec.Command(exe, "ui")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		fail(err)
	}
	fmt.Println("K-Vision is open.")
}

// runningUI lists the process ids of running `kv ui` (the app runs that too).
func runningUI() []int {
	var out []int
	if runtime.GOOS == "windows" {
		// tasklist has no command lines; wmic is gone from new Windows. PowerShell's
		// CIM query has both, and ships with every supported Windows.
		b, err := exec.Command("powershell", "-NoProfile", "-Command",
			`Get-CimInstance Win32_Process -Filter "Name='kv.exe'" | ForEach-Object { "$($_.ProcessId) $($_.CommandLine)" }`).Output()
		if err != nil {
			return out
		}
		me := os.Getpid()
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(strings.TrimSpace(line))
			if len(f) >= 3 && f[len(f)-1] == "ui" {
				if p, err := strconv.Atoi(f[0]); err == nil && p != me {
					out = append(out, p)
				}
			}
		}
		return out
	}
	// Only K-Vision's own binary: the app's (…/K-Vision.app/Contents/MacOS/kv ui) or a
	// kv on the PATH -- the command line must start with a path ending in /kv or be "kv".
	b, err := exec.Command("ps", "-axo", "pid=,args=").Output()
	if err != nil {
		return out
	}
	me := os.Getpid()
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[2] != "ui" {
			continue
		}
		if f[1] != "kv" && !strings.HasSuffix(f[1], "/kv") {
			continue
		}
		if p, err := strconv.Atoi(f[0]); err == nil && p != me {
			out = append(out, p)
		}
	}
	return out
}

func width() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil {
		return w
	}
	return 100
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "kv:", err)
	os.Exit(1)
}

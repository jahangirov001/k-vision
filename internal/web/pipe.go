package web

import (
	"io"
	"sync"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/tools/remotecommand"
)

func newPipe() (*io.PipeReader, *io.PipeWriter) { return io.Pipe() }

// wsWriter sends terminal output to the browser. Writes come from more than one
// goroutine (stdout, and our own error line), and a websocket allows one writer.
type wsWriter struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	marked bool
	got    bool // the remote shell has written something since mark()
}

func (w *wsWriter) mark() { w.mu.Lock(); w.marked = true; w.mu.Unlock() }

func (w *wsWriter) wrote() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.got }

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.marked {
		w.got = true
	}
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// wsSizes feeds the browser's terminal size to the remote TTY.
type wsSizes struct {
	ch chan remotecommand.TerminalSize
}

func (s *wsSizes) Next() *remotecommand.TerminalSize {
	sz, ok := <-s.ch
	if !ok {
		return nil
	}
	return &sz
}

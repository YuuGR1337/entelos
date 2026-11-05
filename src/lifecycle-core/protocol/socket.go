package protocol

// The local socket of the sidecar. POSIX only.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"sync"
	"syscall"
)

// MaxSocketPath is the longest socket path that Listen accepts, in bytes. The
// sun_path field has 104 bytes on macOS and 108 bytes on Linux, with a final
// NUL. A launcher with a longer path can start the sidecar in the socket
// directory and give a relative path.
const MaxSocketPath = 103

// Listen makes a Unix domain socket at path with file mode 0600. If the path
// exists, Listen removes it only if it is a stale socket: a socket file with
// no listener. Listen refuses any other file and a socket with a listener.
func Listen(path string) (*net.UnixListener, error) {
	if len(path) > MaxSocketPath {
		return nil, fmt.Errorf("socket path is longer than %d bytes: start the sidecar in the socket directory and give a relative path", MaxSocketPath)
	}
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode().Type() != fs.ModeSocket:
		return nil, fmt.Errorf("%s exists and is not a socket", path)
	case err == nil:
		conn, dialErr := net.Dial("unix", path)
		if dialErr == nil {
			conn.Close()
			return nil, fmt.Errorf("%s has a listener", path)
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, fmt.Errorf("%s: cannot tell if the socket is stale: %w", path, dialErr)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	// The umask makes the socket file 0600 when bind makes it, so no other
	// user can connect before the chmod.
	old := syscall.Umask(0o177)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	listener.SetUnlinkOnClose(true)
	return listener, nil
}

// Server serves an Adapter on a listener. Each connection has its own
// goroutine. The Adapter sends one Host request at a time.
type Server struct {
	Adapter *Adapter
	mu      sync.Mutex
	conns   map[net.Conn]bool
	wg      sync.WaitGroup
	closed  bool
}

// Serve accepts connections until the listener closes.
func (s *Server) Serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		if s.conns == nil {
			s.conns = map[net.Conn]bool{}
		}
		s.conns[conn] = true
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.Adapter.Serve(conn, conn)
			conn.Close()
			s.mu.Lock()
			delete(s.conns, conn)
			s.mu.Unlock()
		}()
	}
}

// Shutdown closes the listener, which removes the socket file, closes each
// open connection and waits for the connection goroutines.
func (s *Server) Shutdown(listener *net.UnixListener) error {
	s.mu.Lock()
	s.closed = true
	for conn := range s.conns {
		conn.Close()
	}
	s.mu.Unlock()
	// Close removes the socket file before the wait. Shutdown does not remove
	// the path again, because a new sidecar can bind it during the wait.
	err := listener.Close()
	s.wg.Wait()
	return err
}

package protocol

import (
	"io"
	"io/fs"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	lifecycle "example.org/lifecycle-core"
)

// The tests use a relative socket path in a new working directory, because
// the full path of a temporary directory can be longer than sun_path.
const sock = "test.sock"

func inTempDir(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
}

func TestListenMakesSocketWithMode0600(t *testing.T) {
	inTempDir(t)
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	info, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Type() != fs.ModeSocket || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want a socket with 0600", info.Mode())
	}
}

func TestListenRefusesFileThatIsNotSocket(t *testing.T) {
	inTempDir(t)
	os.WriteFile(sock, []byte("data"), 0o600)
	if _, err := Listen(sock); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	if raw, _ := os.ReadFile(sock); string(raw) != "data" {
		t.Fatal("Listen changed the regular file")
	}
	os.Remove(sock)
	os.Mkdir(sock, 0o700)
	if _, err := Listen(sock); err == nil {
		t.Fatal("Listen replaced a directory")
	}
}

func TestListenRefusesLiveSocket(t *testing.T) {
	inTempDir(t)
	first, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Listen(sock); err == nil {
		second.Close()
		t.Fatal("Listen replaced a socket with a listener")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("the first listener does not accept: %v", err)
	}
	conn.Close()
}

func TestListenReplacesStaleSocket(t *testing.T) {
	inTempDir(t)
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatal("the stale socket file is missing")
	}
	l, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen refused a stale socket: %v", err)
	}
	defer l.Close()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestListenRefusesLongPath(t *testing.T) {
	inTempDir(t)
	if _, err := Listen(strings.Repeat("s", MaxSocketPath+1)); err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("got %v, want a path length error", err)
	}
}

func startServer(t *testing.T, host Handler) (*Server, *net.UnixListener, chan error) {
	t.Helper()
	l, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Adapter: NewAdapter(host, loadTestSchemas(t))}
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	return s, l, done
}

func roundTrip(t *testing.T, conn net.Conn, payload []byte) lifecycle.Object {
	t.Helper()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(frameOf(payload)); err != nil {
		t.Fatal(err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, int(header[0])<<24|int(header[1])<<16|int(header[2])<<8|int(header[3]))
	if _, err := io.ReadFull(conn, body); err != nil {
		t.Fatal(err)
	}
	value, err := lifecycle.Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return value.(lifecycle.Object)
}

// Shutdown closes the open connections and removes the socket file.
func TestShutdownRemovesSocket(t *testing.T) {
	inTempDir(t)
	s, l, done := startServer(t, stubHost{result: lifecycle.Object{"cursor": "", "state": lifecycle.Object{}}})
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if r := roundTrip(t, conn, inspect); r["status"] != "ok" || r["request_id"] != "stub" {
		t.Fatalf("got %s", lifecycle.Encode(r))
	}
	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown(l) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown does not return while a connection is open")
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("the socket file stays: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatal("the open connection stays open after shutdown")
	}
}

// gateHost blocks each request until the test releases it.
type gateHost struct{ entered, release chan struct{} }

func (h gateHost) Handle(any) (any, error) {
	h.entered <- struct{}{}
	<-h.release
	return lifecycle.Object{"cursor": "", "state": lifecycle.Object{}}, nil
}

// Two connections are open at the same time, but the Host gets one request
// at a time.
func TestOneHostRequestAtATime(t *testing.T) {
	inTempDir(t)
	h := gateHost{entered: make(chan struct{}, 2), release: make(chan struct{})}
	s, l, _ := startServer(t, h)
	defer s.Shutdown(l)
	// This runs before Shutdown, so that a blocked Host request can return.
	defer close(h.release)
	results := make(chan lifecycle.Object, 2)
	for i := 0; i < 2; i++ {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		go func() { results <- roundTrip(t, conn, inspect) }()
	}
	<-h.entered
	select {
	case <-h.entered:
		t.Fatal("a second Host request started before the first one completed")
	case <-time.After(300 * time.Millisecond):
	}
	h.release <- struct{}{}
	<-h.entered
	h.release <- struct{}{}
	for i := 0; i < 2; i++ {
		if r := <-results; r["status"] != "ok" {
			t.Fatalf("got %s", lifecycle.Encode(r))
		}
	}
}

// Shutdown does not remove a socket that a new sidecar binds while a Host
// request is in flight.
func TestShutdownKeepsReplacementSocket(t *testing.T) {
	inTempDir(t)
	h := gateHost{entered: make(chan struct{}, 1), release: make(chan struct{})}
	s, l, done := startServer(t, h)
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write(frameOf(inspect)); err != nil {
		t.Fatal(err)
	}
	<-h.entered
	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown(l) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(sock); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Shutdown does not close the listener")
		}
		time.Sleep(10 * time.Millisecond)
	}
	replacement, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	close(h.release)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if info, err := os.Lstat(sock); err != nil || info.Mode().Type() != fs.ModeSocket {
		t.Fatalf("Shutdown removed the replacement socket: %v", err)
	}
}

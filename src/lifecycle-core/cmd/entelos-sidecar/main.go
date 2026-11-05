// Command entelos-sidecar serves the wire protocol 1.0 of src/protocol on a
// local Unix domain socket, in front of the Go lifecycle Host. The host
// context comes from a file that the operator selects; a request cannot
// supply it. The only access control is the file mode 0600 of the socket.
//
// When the socket listens, the command writes one JSON line to stdout:
// {"socket":"<path>","status":"listening"}. On SIGINT or SIGTERM, it closes
// the socket, removes the socket file and exits with status 0.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	lifecycle "example.org/lifecycle-core"
	"example.org/lifecycle-core/protocol"
)

func main() { os.Exit(run()) }

func run() int {
	socket := flag.String("socket", "", "socket path; it must not exist, or it must be a stale socket")
	repo := flag.String("repo", "", "dedicated bare Git repository")
	contextPath := flag.String("context", "", "trusted host context, not caller input")
	runtime := flag.String("runtime", "", "pinned CEL executable")
	work := flag.String("work", "", "work directory")
	schema := flag.String("schema", "", "the src/protocol/schema directory")
	flag.Parse()
	if *socket == "" || *repo == "" || *contextPath == "" || *runtime == "" || *work == "" || *schema == "" {
		fmt.Fprintln(os.Stderr, "entelos-sidecar: --socket, --repo, --context, --runtime, --work and --schema are required")
		return 2
	}
	schemas, err := protocol.LoadSchemas(*schema)
	if err != nil {
		return stop(err)
	}
	host, err := open(*repo, *contextPath, *runtime, *work)
	if err != nil {
		return stop(err)
	}
	listener, err := protocol.Listen(*socket)
	if err != nil {
		return stop(err)
	}
	server := &protocol.Server{Adapter: protocol.NewAdapter(host, schemas)}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	os.Stdout.Write(lifecycle.Encode(lifecycle.Object{"status": "listening", "socket": *socket}))
	select {
	case <-signals:
	case err = <-done:
		if err != nil {
			fmt.Fprintln(os.Stderr, "entelos-sidecar:", err)
		}
	}
	if shutdownErr := server.Shutdown(listener); shutdownErr != nil {
		return stop(shutdownErr)
	}
	if err != nil {
		return 1
	}
	return 0
}

func stop(err error) int {
	fmt.Fprintln(os.Stderr, "entelos-sidecar:", err)
	return 1
}

func open(repo, contextPath, binary, work string) (*lifecycle.Host, error) {
	raw, err := os.ReadFile(contextPath)
	if err != nil {
		return nil, err
	}
	context, err := lifecycle.Decode(raw)
	if err != nil {
		return nil, err
	}
	store, err := lifecycle.OpenGitStore(repo)
	if err != nil {
		return nil, err
	}
	rt, err := lifecycle.NewCELRuntime(binary, filepath.Join(work, "tmp"))
	if err != nil {
		return nil, err
	}
	return lifecycle.NewHost(store, rt, context)
}

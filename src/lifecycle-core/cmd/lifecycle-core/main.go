// Command lifecycle-core is the JSONL host of the Go lifecycle core. The host
// selects the context and the runtime; a request cannot supply authority.
// It has the same arguments and wire form as src/cel-lifecycle/cli.py.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	lifecycle "example.org/lifecycle-core"
)

const maxRequest = 1048576

func main() { os.Exit(run()) }

func run() int {
	repo := flag.String("repo", "", "dedicated bare Git repository")
	context := flag.String("context", "", "trusted host context, not caller input")
	runtime := flag.String("runtime", "", "pinned CEL executable")
	work := flag.String("work", "", "work directory")
	flag.Parse()
	if *repo == "" || *context == "" || *runtime == "" || *work == "" {
		fmt.Fprintln(os.Stderr, "lifecycle-core: --repo, --context, --runtime and --work are required")
		return 2
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	write := func(value any) {
		out.Write(lifecycle.Encode(value))
		out.Flush()
	}
	host, err := open(*repo, *context, *runtime, *work)
	if err != nil {
		write(configuration(err))
		return 1
	}
	in := bufio.NewReaderSize(os.Stdin, maxRequest+1)
	for {
		raw, err := readLine(in)
		if len(raw) == 0 && err != nil {
			break
		}
		if len(raw) > maxRequest {
			write(lifecycle.NewFault("invalid_request", "request exceeds reference byte bound; stream closed", "").Record())
			return 1
		}
		request, derr := lifecycle.Decode(raw)
		var result any
		if derr == nil {
			result, derr = host.Handle(request)
		}
		var fault *lifecycle.Fault
		switch {
		case derr == nil:
			write(lifecycle.Object{"status": "ok", "data": result})
		case errors.As(derr, &fault):
			write(fault.Record())
		default:
			write(configuration(derr))
			return 1
		}
	}
	return 0
}

// readLine reads one line of at most maxRequest+1 bytes, like readline(size).
func readLine(in *bufio.Reader) ([]byte, error) {
	var line []byte
	for len(line) <= maxRequest {
		b, err := in.ReadByte()
		if err != nil {
			return line, err
		}
		line = append(line, b)
		if b == '\n' {
			return line, nil
		}
	}
	return line, nil
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

func configuration(err error) lifecycle.Object {
	var fault *lifecycle.Fault
	if errors.As(err, &fault) {
		return fault.Record()
	}
	return lifecycle.Object{"status": "configuration_error", "message": err.Error()}
}

package lifecycle

// The CEL runtime adapter calls the pinned CEL executable with explicit
// bundles, as the Python reference does. The Host never evaluates CEL itself.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Bundle file names and media types.
var (
	FileNames = map[string]string{"manifest": "manifest.json", "environment": "env.json", "source": "rule.cel", "fixed": "fixed.json"}
	Mimes     = map[string]string{"manifest": "application/vnd.cel-handoff.bundle+json", "environment": "application/json", "source": "text/plain", "fixed": "application/json"}
)

// Runtime evaluates bundles. Execute gives the decoded executable result.
type Runtime interface {
	Build() (string, error)
	Execute(files map[string][]byte, command string, inputs []byte, cases any) (any, error)
}

// CELRuntime runs the pinned CEL executable.
type CELRuntime struct {
	Binary, Work string
}

// NewCELRuntime makes the work directory.
func NewCELRuntime(binary, work string) (*CELRuntime, error) {
	b, err := filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	w, err := filepath.Abs(work)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(w, 0o777); err != nil {
		return nil, err
	}
	return &CELRuntime{Binary: b, Work: w}, nil
}

func (r *CELRuntime) call(args []string, data []byte) (int, any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	if data != nil {
		cmd.Stdin = bytes.NewReader(data)
	}
	var stdout bytes.Buffer
	stderr := &boundedBuffer{limit: maxStderr}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	err := cmd.Run()
	unavailable := func(message string) error {
		if text := strings.TrimSpace(stderr.String()); text != "" {
			message += "; stderr: " + text
		}
		return NewFault("implementation_unavailable", "pinned CEL process unavailable: "+message, "runtime")
	}
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || ctx.Err() != nil) {
		return 0, nil, unavailable(err.Error())
	}
	value, derr := Decode(stdout.Bytes())
	if derr != nil {
		return 0, nil, unavailable(derr.(*Fault).Message)
	}
	return cmd.ProcessState.ExitCode(), value, nil
}

// maxStderr is the number of stderr bytes that a fault message can hold.
const maxStderr = 4096

// boundedBuffer keeps the first limit bytes and discards the rest. It does
// not embed bytes.Buffer, so that io.Copy cannot use ReadFrom past the limit.
type boundedBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	room := b.limit - len(b.data)
	if room < n {
		b.truncated = true
		p = p[:max(room, 0)]
	}
	b.data = append(b.data, p...)
	return n, nil
}

func (b *boundedBuffer) String() string {
	if b.truncated {
		return string(b.data) + " [truncated]"
	}
	return string(b.data)
}

func (r *CELRuntime) Build() (string, error) {
	code, value, err := r.call([]string{"-build"}, nil)
	if err != nil {
		return "", err
	}
	ref, _ := lookupAny(value, "implementation_ref").(string)
	if code != 0 || ref == "" {
		return "", NewFault("implementation_unavailable", "missing build identity", "runtime")
	}
	return ref, nil
}

func (r *CELRuntime) Execute(files map[string][]byte, command string, inputs []byte, cases any) (any, error) {
	dir, err := os.MkdirTemp(r.Work, "tmp")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	for key, name := range FileNames {
		if err := os.WriteFile(filepath.Join(dir, name), files[key], 0o666); err != nil {
			return nil, err
		}
	}
	args := []string{"-bundle", dir}
	if cases != nil {
		path := filepath.Join(dir, "cases.json")
		if err := os.WriteFile(path, Encode(cases), 0o666); err != nil {
			return nil, err
		}
		args = append(args, "-cases", path)
	}
	args = append(args, command)
	var data []byte
	if inputs != nil {
		decoded, err := Decode(inputs)
		if err != nil {
			return nil, err
		}
		ref := ContentRef(Envelope(Mimes["manifest"], files["manifest"]))
		data = Encode(Object{"bundle_ref": ref, "inputs": decoded})
	}
	code, value, err := r.call(args, data)
	if err != nil {
		return nil, err
	}
	// Only describe treats a nonzero exit as a fault, as in cel_runtime.py.
	// check exits 1 with a JSON report when a case fails; the Host records
	// that report as a failed qualification. run gives a JSON response that
	// the Host checks for the expected bundle and build.
	if command == "describe" && code != 0 {
		status, ok := lookupAny(value, "status").(string)
		if !ok {
			status = "runtime_error"
		}
		message, ok := lookupAny(lookupAny(value, "diagnostic"), "message").(string)
		if !ok {
			message = "load failed"
		}
		return nil, NewFault(status, message, "runtime")
	}
	return value, nil
}

// lookupAny is value.get(key) that gives nil for a non-object.
func lookupAny(value any, key string) any {
	if m, ok := value.(Object); ok {
		return m[key]
	}
	return nil
}

package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"golang.org/x/term"
)

const WorkerArgument = "__keychain-worker"

// Keychain invokes the Security framework in a bounded child process. Secrets
// travel only over anonymous inherited pipes, never argv, environment or stdout.
type Keychain struct {
	executable  string
	args        []string
	path        string // nonempty only for disposable-keychain integration tests
	timeout     time.Duration
	interactive bool
}

func NewKeychain() *Keychain {
	executable, _ := os.Executable()
	return &Keychain{executable: executable, args: []string{WorkerArgument}, timeout: 8 * time.Second}
}

// NewInteractiveKeychain is an explicit foreground opt-in. macOS, not this
// program, asks the user to authorize each protected item. Normal operations
// retain their noninteractive, eight-second worker and never change ACLs.
func NewInteractiveKeychain() (*Keychain, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, ErrInteraction
	}
	defer f.Close()
	if !term.IsTerminal(int(f.Fd())) {
		return nil, ErrInteraction
	}
	k := NewKeychain()
	k.interactive, k.timeout = true, 60*time.Second
	return k, nil
}

type workerRequest struct {
	Action      string `json:"action"`
	Ref         Ref    `json:"ref"`
	Value       []byte `json:"value,omitempty"`
	Replace     bool   `json:"replace,omitempty"`
	Keychain    string `json:"keychain,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
}
type workerResponse struct {
	Status int    `json:"status"`
	Value  []byte `json:"value,omitempty"`
}

func (k *Keychain) call(ctx context.Context, request workerRequest) (workerResponse, error) {
	var result workerResponse
	if !request.Ref.Valid() || k.executable == "" {
		return result, ErrInvalid
	}
	request.Keychain = k.path
	request.Interactive = k.interactive && request.Action == "get"
	data, err := json.Marshal(request)
	if err != nil {
		return result, ErrInvalid
	}
	defer clear(data)
	input, send, err := os.Pipe()
	if err != nil {
		return result, ErrUnavailable
	}
	defer input.Close()
	defer send.Close()
	receive, output, err := os.Pipe()
	if err != nil {
		return result, ErrUnavailable
	}
	defer receive.Close()
	defer output.Close()
	ctx, cancel := context.WithTimeout(ctx, k.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, k.executable, k.args...)
	cmd.ExtraFiles = []*os.File{input, output}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	cmd.WaitDelay = time.Second
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if cmd.Start() != nil {
		return result, ErrUnavailable
	}
	input.Close()
	output.Close()
	written := make(chan error, 1)
	go func() { _, e := send.Write(data); send.Close(); written <- e }()
	response, readErr := io.ReadAll(io.LimitReader(receive, 3*MaxSecret+1))
	err = cmd.Wait()
	send.Close()
	writeErr := <-written
	defer clear(response)
	if ctx.Err() != nil {
		if request.Action == "get" {
			return result, &Error{"keychain_timeout", "not_sent"}
		}
		return result, ErrTimeout
	}
	var wire struct {
		Status *int   `json:"status"`
		Value  []byte `json:"value,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	if err != nil || readErr != nil || writeErr != nil || len(response) > 3*MaxSecret || decoder.Decode(&wire) != nil || wire.Status == nil {
		return result, &Error{"keychain_worker_failed", "unknown"}
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return result, &Error{"keychain_worker_failed", "unknown"}
	}
	result.Status, result.Value = *wire.Status, wire.Value
	return result, classifyStatus(result.Status)
}
func (k *Keychain) Get(ctx context.Context, ref Ref) (*Secret, error) {
	r, err := k.call(ctx, workerRequest{Action: "get", Ref: ref})
	defer clear(r.Value)
	if err != nil {
		return nil, err
	}
	return NewSecret(r.Value)
}
func (k *Keychain) Put(ctx context.Context, ref Ref, secret *Secret, replace bool) error {
	if secret == nil || len(secret.data) == 0 || len(secret.data) > MaxSecret {
		return ErrInvalid
	}
	_, err := k.call(ctx, workerRequest{Action: "put", Ref: ref, Value: secret.data, Replace: replace})
	return err
}
func (k *Keychain) Delete(ctx context.Context, ref Ref) error {
	_, err := k.call(ctx, workerRequest{Action: "delete", Ref: ref})
	return err
}

// ServeWorker is called before normal CLI parsing, and refuses ordinary terminal
// descriptors. Its response can contain a secret, so it never uses stdout/stderr.
func ServeWorker(input, output *os.File) int {
	for _, f := range []*os.File{input, output} {
		if f == nil {
			return 2
		}
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
			return 2
		}
	}
	data, err := io.ReadAll(io.LimitReader(input, 3*MaxSecret+1))
	defer clear(data)
	if err != nil || len(data) > 3*MaxSecret {
		return 2
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var q workerRequest
	if decoder.Decode(&q) != nil {
		return 2
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || !q.Ref.Valid() {
		return 2
	}
	defer clear(q.Value)
	if q.Action != "get" && q.Action != "put" && q.Action != "delete" {
		return 2
	}
	if q.Interactive {
		if q.Action != "get" {
			return 2
		}
		f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return 2
		}
		valid := term.IsTerminal(int(f.Fd()))
		f.Close()
		if !valid {
			return 2
		}
	}
	if q.Action == "put" && (len(q.Value) == 0 || len(q.Value) > MaxSecret) {
		return 2
	}
	response := nativeKeychain(q)
	defer clear(response.Value)
	if json.NewEncoder(output).Encode(response) != nil {
		return 5
	}
	return 0
}

package landedwork

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// objectBatch serves object reads from one long-lived `git cat-file --batch`
// process. A process per object dominated proof time: one 100-commit interval
// started about 14,000 of them, mostly to read blobs.
//
// Reads charge the meter exactly as the per-object commands did: a blob read
// charges its content, an existence check charges nothing.
type objectBatch struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	broken error
}

var errObjectUnavailable = errors.New("object unavailable")

// blob returns the content of a blob object, as `cat-file blob` would.
func (v *objectView) blob(ctx context.Context, id string) ([]byte, error) {
	return v.readObject(ctx, id, "blob", true)
}

// hasCommit verifies the commit object physically, as `cat-file -e id^{commit}`.
func (v *objectView) hasCommit(ctx context.Context, id string) error {
	_, err := v.readObject(ctx, id+"^{commit}", "commit", false)
	return err
}

func (v *objectView) readObject(ctx context.Context, name, kind string, charge bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if v.meter.failed {
		return nil, ErrInputBudget
	}
	if strings.ContainsAny(name, " \t\r\n") {
		return nil, errObjectUnavailable
	}
	b := v.objects
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := b.read(ctx, v, name, kind, charge)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, ErrInputBudget) || v.meter.failed {
		return nil, ErrInputBudget
	}
	if err != nil {
		return nil, fmt.Errorf("read Git objects: %w", err)
	}
	return data, nil
}

func (b *objectBatch) start(ctx context.Context, v *objectView) error {
	cmd := v.command(ctx, "cat-file", "--batch")
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = &boundedBuffer{meter: v.meter}
	if err = cmd.Start(); err != nil {
		return err
	}
	b.cmd, b.in, b.out = cmd, in, bufio.NewReader(out)
	return nil
}

func (b *objectBatch) read(ctx context.Context, v *objectView, name, kind string, charge bool) ([]byte, error) {
	if b.broken != nil {
		return nil, b.broken
	}
	if b.cmd == nil {
		if err := b.start(ctx, v); err != nil {
			b.broken = err
			return nil, err
		}
	}
	data, err := b.exchange(v, name, kind, charge)
	if err != nil && !errors.Is(err, errObjectUnavailable) {
		// The stream position is unknown; never reuse it.
		b.broken = err
		b.stop()
	}
	return data, err
}

func (b *objectBatch) exchange(v *objectView, name, kind string, charge bool) ([]byte, error) {
	if _, err := io.WriteString(b.in, name+"\n"); err != nil {
		return nil, err
	}
	header, err := b.out.ReadString('\n')
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && fields[0] == name && (fields[1] == "missing" || fields[1] == "ambiguous") {
		return nil, errObjectUnavailable
	}
	if len(fields) != 3 || !objectID(fields[0]) {
		return nil, errors.New("invalid object batch header")
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || size < 0 {
		return nil, errors.New("invalid object batch size")
	}
	if fields[1] != kind {
		// The per-object command failed here without printing the object.
		if _, err = io.CopyN(io.Discard, b.out, size+1); err != nil {
			return nil, err
		}
		return nil, errObjectUnavailable
	}
	if charge {
		if err = v.meter.input(size); err != nil {
			return nil, err
		}
	}
	data := make([]byte, size+1)
	if _, err = io.ReadFull(b.out, data); err != nil {
		return nil, err
	}
	if data[size] != '\n' {
		return nil, errors.New("invalid object batch terminator")
	}
	return data[:size], nil
}

func (b *objectBatch) stop() {
	if b.cmd == nil {
		return
	}
	_ = b.in.Close()
	if b.broken != nil && b.cmd.Process != nil {
		_ = b.cmd.Process.Kill()
	}
	// Every returned object was fully read and checked; exit status adds nothing.
	_ = b.cmd.Wait()
	b.cmd = nil
}

func (b *objectBatch) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stop()
}

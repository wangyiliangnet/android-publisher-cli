package credentials

import (
	"context"
	"io"
	"os"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func ReadInput(input io.Reader) (*Secret, error) {
	data, err := io.ReadAll(io.LimitReader(input, MaxSecret+1))
	defer clear(data)
	if err != nil {
		return nil, ErrInvalid
	}
	return NewSecret(data)
}

func ReadPipe(ctx context.Context, input *os.File) (*Secret, error) {
	if term.IsTerminal(int(input.Fd())) {
		return nil, ErrInvalid
	}
	fd := int(input.Fd())
	flags, err := unix.FcntlInt(input.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	if unix.SetNonblock(fd, true) != nil {
		return nil, ErrInvalid
	}
	defer unix.FcntlInt(input.Fd(), unix.F_SETFL, flags)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var data []byte
	defer func() { clear(data) }()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, ErrInvalid
		}
		if n == 0 {
			continue
		}
		var buf [4096]byte
		n, err = unix.Read(fd, buf[:])
		if err == unix.EAGAIN {
			continue
		}
		if err != nil {
			return nil, ErrInvalid
		}
		if n == 0 {
			return NewSecret(data)
		}
		data = append(data, buf[:n]...)
		clear(buf[:])
		if len(data) > MaxSecret {
			return nil, ErrInvalid
		}
	}
}

// Prompt uses a controlling terminal in raw/no-echo mode, with polling so
// cancellation restores terminal settings. Multiline values use file/stdin.
func Prompt(ctx context.Context) (*Secret, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, ErrInteraction
	}
	defer f.Close()
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return nil, ErrInteraction
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, ErrInteraction
	}
	defer term.Restore(fd, state)
	if unix.SetNonblock(fd, true) != nil {
		return nil, ErrInteraction
	}
	defer unix.SetNonblock(fd, false)
	f.WriteString("Secret (hidden): ")
	defer f.WriteString("\r\n")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var data []byte
	defer func() { clear(data) }()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ready, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, ErrInteraction
		}
		if ready == 0 {
			continue
		}
		var one [1]byte
		n, err := unix.Read(fd, one[:])
		if err == unix.EAGAIN {
			continue
		}
		if err != nil || n == 0 {
			return nil, ErrInvalid
		}
		switch one[0] {
		case '\r', '\n':
			return NewSecret(data)
		case 3, 4:
			return nil, context.Canceled
		case 127, 8:
			if len(data) > 0 {
				_, size := utf8.DecodeLastRune(data)
				clear(data[len(data)-size:])
				data = data[:len(data)-size]
			}
		default:
			if len(data) >= MaxSecret {
				return nil, ErrInvalid
			}
			data = append(data, one[0])
		}
	}
}

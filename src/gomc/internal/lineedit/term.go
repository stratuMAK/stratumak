// Copyright (C) 2026 Sascha Ittner <sascha.ittner@modusoft.de>
// License: GPL Version 2

package lineedit

import (
	"os"
	"syscall"
	"unsafe"
)

// fdTerm is the real terminal control implementation, talking termios to a tty
// file descriptor.  It is deliberately built on the stdlib syscall package so
// that interactive editing pulls in no new module dependency.
type fdTerm struct {
	fd uintptr
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// IsTerminal reports whether f is a character device we can put into raw mode.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var t syscall.Termios
	return ioctl(f.Fd(), syscall.TCGETS, unsafe.Pointer(&t)) == nil
}

// MakeRaw switches the terminal to character-at-a-time, no-echo mode and
// returns a function restoring the previous settings.  The restore closure is
// idempotent-safe to call once; callers defer it.
func (t fdTerm) MakeRaw() (func(), error) {
	var oldState syscall.Termios
	if err := ioctl(t.fd, syscall.TCGETS, unsafe.Pointer(&oldState)); err != nil {
		return nil, err
	}

	raw := oldState
	// Mirror cfmakeraw() minus OPOST: keeping output post-processing off would
	// force us to emit \r\n by hand everywhere, and we already do that for the
	// lines we print, so drop OPOST too for predictable cursor arithmetic.
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0

	if err := ioctl(t.fd, syscall.TCSETS, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	restore := oldState
	return func() {
		_ = ioctl(t.fd, syscall.TCSETS, unsafe.Pointer(&restore))
	}, nil
}

// winsize mirrors struct winsize for TIOCGWINSZ.
type winsize struct {
	rows, cols, xpixel, ypixel uint16
}

// Columns returns the terminal width, falling back to 80 when the ioctl fails
// or reports a nonsense value (e.g. a pty that has not been sized yet).
func (t fdTerm) Columns() int {
	var ws winsize
	if err := ioctl(t.fd, syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil {
		return defaultColumns
	}
	if ws.cols < 20 {
		return defaultColumns
	}
	return int(ws.cols)
}

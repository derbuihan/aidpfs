// Package backend defines filesystem operations independently of the remote service.
package backend

import (
	"context"
	"time"
)

type Kind uint8

const (
	Directory Kind = iota + 1
	File
	Notebook
)

type Metadata struct {
	Name     string
	Kind     Kind
	Size     int64
	Modified time.Time
	Version  string
}

// Reader paths are relative to a configured root, with the empty string denoting
// that root. Implementations must reject paths that escape it.
type Reader interface {
	Stat(context.Context, string) (Metadata, error)
	ReadDir(context.Context, string) ([]Metadata, error)
	ReadFile(context.Context, string) ([]byte, error)
}

// NotAppliedError marks a failed mutation that definitely did not change the
// remote workspace: either no request was sent, or the server rejected it
// before applying it (for example an invalid name or an existing destination).
// Callers may treat the operation as resolved instead of unknown.
type NotAppliedError struct{ Err error }

func (e *NotAppliedError) Error() string { return e.Err.Error() }
func (e *NotAppliedError) Unwrap() error { return e.Err }

// NotApplied wraps err as a NotAppliedError; nil stays nil.
func NotApplied(err error) error {
	if err == nil {
		return nil
	}
	return &NotAppliedError{Err: err}
}

// ValidName reports whether name satisfies the object name rule observed in
// the tested tenant for create and rename targets: ASCII letters, digits,
// underscore, dot, plus, hyphen, at-sign and space, at least one character.
// "." and ".." are never valid names.
func ValidName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '+' || c == '-' || c == '@' || c == ' ':
		default:
			return false
		}
	}
	return true
}

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

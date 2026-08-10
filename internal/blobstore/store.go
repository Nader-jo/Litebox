// Package blobstore provides private provider-neutral durable byte storage.
package blobstore

import (
	"context"
	"io"
	"time"
)

// BlobInfo describes immutable stored content.
type BlobInfo struct {
	Key         string
	Size        int64
	SHA256      string
	ContentType string
	ModTime     time.Time
}

// Store is the only byte-storage contract used by mailbox services.
type Store interface {
	Put(context.Context, string, io.Reader, int64, string) (BlobInfo, error)
	Get(context.Context, string) (io.ReadCloser, BlobInfo, error)
	Delete(context.Context, string) error
	Exists(context.Context, string) (bool, error)
	Health(context.Context) error
}

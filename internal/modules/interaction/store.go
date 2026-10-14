package interaction

import (
	"context"
	"fmt"
	"strings"

	"github.com/minio/minio-go/v7"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/media"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/storage"
)

// ObjectStore is what the module needs from object storage (faked in tests).
type ObjectStore interface {
	media.ObjectStore
	// DeletePrefix removes every object under prefix ("scorm/<uuid>/"),
	// Storage::deleteDirectory. A missing prefix is not an error.
	DeletePrefix(ctx context.Context, prefix string) error
}

// minioStore adds DeletePrefix to *storage.Storage.
type minioStore struct{ *storage.Storage }

func (s minioStore) DeletePrefix(ctx context.Context, prefix string) error {
	prefix = storage.CleanKey(prefix)
	if prefix == "" || !strings.HasSuffix(prefix, "/") {
		return fmt.Errorf("interaction: refusing to delete prefix %q", prefix)
	}
	cl, bucket := s.Client(), s.Bucket()
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var listErr error
	objs := make(chan minio.ObjectInfo)
	go func() {
		defer close(objs)
		for o := range cl.ListObjects(listCtx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if o.Err != nil {
				listErr = o.Err
				return
			}
			select {
			case objs <- o:
			case <-listCtx.Done():
				return
			}
		}
	}()
	var rmErr error
	for e := range cl.RemoveObjects(ctx, bucket, objs, minio.RemoveObjectsOptions{}) {
		if rmErr == nil && e.Err != nil {
			rmErr = fmt.Errorf("storage: delete %q: %w", e.ObjectName, e.Err)
		}
	}
	if listErr != nil {
		return fmt.Errorf("storage: list %q: %w", prefix, listErr)
	}
	return rmErr
}

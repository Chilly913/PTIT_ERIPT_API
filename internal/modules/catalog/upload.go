package catalog

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"sort"
	"strings"
)

var errNoStore = errors.New("catalog: object storage is not configured")

// imageMimes is Laravel 13's `image` rule (jpg, jpeg, png, gif, bmp, webp; no svg).
var imageMimes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/bmp": true, "image/x-ms-bmp": true, "image/webp": true,
}

// sniff detects the content type from the first 512 bytes.
func sniff(fh *multipart.FileHeader) string {
	f, err := fh.Open()
	if err != nil {
		return ""
	}
	defer f.Close()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	return http.DetectContentType(head[:n])
}

// fileOf returns the uploaded file of a field (nil when none).
func fileOf(in *input, key string) *multipart.FileHeader {
	fh, _ := in.get(key).(*multipart.FileHeader)
	return fh
}

// uploadKey is MediaService::upload's file name: <folder>/<time>_<random10>.<ext>.
func (m *Module) uploadKey(folder, filename string) string {
	ext := strings.TrimPrefix(path.Ext(filename), ".")
	return fmt.Sprintf("%s/%d_%s.%s", folder, m.now().Unix(), randomAlnum(10), ext)
}

// putUpload stores an uploaded file under folder and returns its object key.
func (m *Module) putUpload(ctx context.Context, fh *multipart.FileHeader, folder string) (string, error) {
	if m.store == nil {
		return "", errNoStore
	}
	key := m.uploadKey(folder, fh.Filename)
	f, err := fh.Open()
	if err != nil {
		return "", fmt.Errorf("catalog: open upload: %w", err)
	}
	defer f.Close()
	if err := m.store.Put(ctx, key, f, fh.Size, sniff(fh)); err != nil {
		return "", fmt.Errorf("catalog: store upload: %w", err)
	}
	return key, nil
}

const alnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func randomAlnum(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = alnum[int(b[i])%len(alnum)]
	}
	return string(b)
}

func sortStrings(s []string) { sort.Strings(s) }

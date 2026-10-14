package auth

import (
	"crypto/rand"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/Chilly913/PTIT_ERIPT_API/internal/auth"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/httpx"
	"github.com/Chilly913/PTIT_ERIPT_API/internal/logx"
)

const maxUploadKB = 5120

// imageMimes is Laravel 13's `image` rule (jpg, jpeg, png, gif, bmp, webp; no svg).
var imageMimes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/gif": true, "image/bmp": true, "image/x-ms-bmp": true, "image/webp": true,
}

var uploadFolders = map[string]string{
	"users":    "users/avatars",
	"settings": "admin/settings",
	"courses":  "admin/courses",
	"faces":    "admin/faces",
}

// uploadMedia is POST /auth/upload (multipart: file, folder?, old_path?).
func (m *Module) uploadMedia(c *gin.Context) {
	ctx := c.Request.Context()
	u, ok := m.currentUser(c)
	if !ok {
		return
	}
	if err := c.Request.ParseMultipartForm(8 << 20); err != nil && err != http.ErrNotMultipart {
		httpx.Abort(c, http.StatusBadRequest, "The request contains invalid form values.")
		return
	}
	bag := httpx.NewValidationError()
	var fh *multipart.FileHeader
	var head []byte
	if f, h, err := c.Request.FormFile("file"); err != nil {
		if c.Request.MultipartForm != nil && len(c.Request.MultipartForm.Value["file"]) > 0 && c.Request.MultipartForm.Value["file"][0] != "" {
			bag.AddRule("file", "file", nil)
			bag.AddRule("file", "image", nil)
		} else {
			bag.AddRule("file", "required", nil)
		}
	} else {
		fh = h
		head = make([]byte, 512)
		n, _ := io.ReadFull(f, head)
		head = head[:n]
		_ = f.Close()
		if !imageMimes[http.DetectContentType(head)] {
			bag.AddRule("file", "image", nil)
		}
		if float64(h.Size)/1024 > maxUploadKB {
			bag.AddRule("file", "max.file", map[string]string{"max": fmt.Sprint(maxUploadKB)})
		}
	}
	folderKey := strings.TrimSpace(c.PostForm("folder"))
	if folderKey != "" {
		if _, ok := uploadFolders[folderKey]; !ok {
			bag.AddRule("folder", "in", nil)
		}
	}
	oldPath := strings.TrimSpace(c.PostForm("old_path"))
	if utf8.RuneCountInString(oldPath) > 2048 {
		bag.AddRule("old_path", "max.string", map[string]string{"max": "2048"})
	}
	if bag.HasErrors() {
		httpx.Fail(c, bag)
		return
	}
	if folderKey == "" {
		folderKey = "users"
	}

	allowed := true
	if folderKey != "users" {
		roles, perms, err := m.roles.UserRolesPermissions(ctx, u.ID)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		isAdmin := auth.HasRole(roles, auth.RoleAdmin)
		switch folderKey {
		case "courses":
			allowed = isAdmin || auth.HasRole(perms, "manage_courses")
		default: // settings, faces
			allowed = isAdmin
		}
	}
	if !allowed {
		httpx.Abort(c, http.StatusForbidden, "Forbidden")
		return
	}
	if m.store == nil {
		httpx.Fail(c, fmt.Errorf("upload: object storage is not configured"))
		return
	}

	folder := uploadFolders[folderKey]
	ext := strings.TrimPrefix(path.Ext(fh.Filename), ".")
	key := fmt.Sprintf("%s/%d_%s.%s", folder, m.now().Unix(), randomAlnum(10), ext)
	mimeType := http.DetectContentType(head)
	f, err := fh.Open()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	defer f.Close()
	if err := m.store.Put(ctx, key, f, fh.Size, mimeType); err != nil {
		httpx.Fail(c, err)
		return
	}

	if oldPath != "" {
		// Deliberate hardening: legacy deleted any key; only objects inside the
		// folder being uploaded to may be removed.
		oldKey := m.media.StorageKey(oldPath)
		if strings.HasPrefix(oldKey, folder+"/") && !strings.Contains(oldKey, "seed/avatars") {
			if err := m.store.Delete(ctx, oldKey); err != nil {
				logx.FromContext(ctx).WarnContext(ctx, "delete old upload failed", "key", oldKey, "error", err.Error())
			}
		}
	}

	c.JSON(http.StatusCreated, gin.H{
		"message": "Upload successful",
		"path":    key,
		"url":     m.media.URL(key),
		"meta": gin.H{
			"path": key,
			"name": fh.Filename,
			"size": math.Round(float64(fh.Size)/1024/1024*100) / 100,
			"mime": mimeType,
			"disk": "minio",
		},
	})
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

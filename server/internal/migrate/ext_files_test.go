package migrate

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// SEC-017: путь из базы старого Laravel — недоверенный; файл читается только внутри --laravel-storage,
// включая символические ссылки, ведущие наружу.
func TestLoadImageStaysInStorage(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.png")
	if err := os.WriteFile(secret, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "club-logos"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "club-logos", "ok.png"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "club-logos", "link.png")); err != nil {
		t.Skip("symlink:", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir")); err != nil {
		t.Fatal(err)
	}

	if _, err := loadImage(root, "club-logos/ok.png", 1<<20); err != nil {
		t.Fatalf("ok.png: %v", err)
	}
	for _, rel := range []string{"club-logos/link.png", "dir/secret.png", "../" + filepath.Base(outside) + "/secret.png", "/etc/passwd"} {
		_, err := loadImage(root, rel, 1<<20)
		var bad errImage
		if !errors.As(err, &bad) {
			t.Errorf("%s: want rejection, got %v", rel, err)
		}
	}
}

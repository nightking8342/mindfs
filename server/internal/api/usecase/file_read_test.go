package usecase

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mindfs/server/internal/fs"
)

func TestFileReadsInsideAndOutsideProject(t *testing.T) {
	root := fs.NewRootInfo("mindfs", "mindfs", t.TempDir())
	service := Service{Registry: uploadTestRegistry{root: root}}
	externalDir := t.TempDir()
	for _, dir := range []string{root.RootPath, externalDir} {
		if err := os.WriteFile(filepath.Join(dir, "REPORT.md"), []byte("report content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{"REPORT.md", filepath.Join(root.RootPath, "REPORT.md"), filepath.Join(externalDir, "REPORT.md")} {
		t.Run(input, func(t *testing.T) {
			wantPath := "REPORT.md"
			if input == filepath.Join(externalDir, "REPORT.md") {
				wantPath = filepath.ToSlash(input)
			}
			for _, mode := range []string{"full", "incremental"} {
				out, err := service.ReadFile(context.Background(), ReadFileInput{RootID: root.ID, Path: input, ReadMode: mode})
				if err != nil {
					t.Fatal(err)
				}
				if out.File.Path != wantPath || out.File.Root != root.ID || out.File.Content != "report content" {
					t.Fatalf("unexpected read: %+v", out.File)
				}
			}
			info, err := service.GetFileInfo(context.Background(), GetFileInfoInput{RootID: root.ID, Path: input})
			if err != nil || info.Path != wantPath || info.Size != 14 {
				t.Fatalf("unexpected info: %+v, %v", info, err)
			}
			raw, err := service.OpenFileRaw(context.Background(), OpenFileRawInput{RootID: root.ID, Path: input})
			if err != nil {
				t.Fatal(err)
			}
			defer raw.File.Close()
			content, err := io.ReadAll(raw.File)
			if err != nil || string(content) != "report content" || raw.RelPath != wantPath {
				t.Fatalf("unexpected raw: %q, %q, %v", content, raw.RelPath, err)
			}
		})
	}
	_, err := service.ReadFile(context.Background(), ReadFileInput{RootID: root.ID, Path: filepath.Join(externalDir, "missing.md")})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error: %v", err)
	}
	_, err = service.ReadFile(context.Background(), ReadFileInput{RootID: root.ID, Path: "../outside.md"})
	if err == nil {
		t.Fatal("relative traversal must remain invalid")
	}
	_, err = service.ReadFile(context.Background(), ReadFileInput{RootID: "missing-root", Path: filepath.Join(externalDir, "REPORT.md")})
	if err == nil {
		t.Fatal("external reads must still require a valid managed root")
	}
	if _, err := os.Stat(filepath.Join(externalDir, ".mindfs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external read must not create metadata: %v", err)
	}
}

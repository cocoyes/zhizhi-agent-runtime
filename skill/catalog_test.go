package skill

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func fixture(t *testing.T, root, name, description string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte("---\nname: "+name+"\ndescription: "+description+"\n---\nSelected instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCatalogAtomicReloadAndDrift(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := fixture(t, root, "pdf", "读取 PDF 文档")
	c := NewCatalog(CatalogOptions{})
	first, err := c.LoadRoot(ctx, root)
	if err != nil || first.Added != 1 {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := c.LoadRoot(ctx, root)
	if err != nil || second.Generation != first.Generation || second.Unchanged != 1 {
		t.Fatalf("%+v %v", second, err)
	}
	meta := c.Metadata()[0]
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = c.LoadRoot(ctx, root); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("%v", err)
	}
	if len(c.Metadata()) != 1 {
		t.Fatal("failed reload discarded old snapshot")
	}
	if _, err = c.Load(ctx, "pdf", meta.Fingerprint); !errors.Is(err, ErrCatalogDrift) {
		t.Fatalf("%v", err)
	}
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	report, err := c.LoadRoot(ctx, root)
	if err != nil || report.Removed != 1 {
		t.Fatalf("%+v %v", report, err)
	}
}

func TestSearchBoundedMultilingualAndDuplicate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fixture(t, root, "pdf", "读取和处理 PDF 文档")
	fixture(t, root, "sheets", "分析电子表格 spreadsheet")
	c := NewCatalog(CatalogOptions{})
	if _, err := c.LoadRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"PDF", "读取文档", "处理 PDF"} {
		hits, err := c.Search(ctx, query, 1)
		if err != nil || len(hits) != 1 || hits[0].Name != "pdf" {
			t.Fatalf("%s: %+v %v", query, hits, err)
		}
	}
	other := t.TempDir()
	fixture(t, other, "pdf", "duplicate")
	if _, err := c.LoadRoot(ctx, other); !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("%v", err)
	}
	if len(c.Metadata()) != 2 {
		t.Fatal("duplicate changed catalog")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = c.Search(ctx, "pdf", 5); _, _ = c.LoadRoot(ctx, root) }()
	}
	wg.Wait()
}

func TestResourcesConfinedAndBounded(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fixture(t, root, "pdf", "PDF")
	c := NewCatalog(CatalogOptions{})
	if _, err := c.LoadRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	loaded, err := c.Load(ctx, "pdf", c.Metadata()[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(external, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(loaded.Dir, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret", external, "escape"} {
		if _, _, err := loaded.ReadResource(ctx, path, 1024); !errors.Is(err, ErrResourceDenied) {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestLoadCancellationAndFrontMatterAtEOF(t *testing.T) {
	root := t.TempDir()
	path := fixture(t, root, "pdf", "PDF")
	c := NewCatalog(CatalogOptions{})
	ctx := context.Background()
	if _, err := c.LoadRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	loaded, err := c.Load(ctx, "pdf", c.Metadata()[0].Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Load(cancelled, "pdf", loaded.Fingerprint); !errors.Is(err, context.Canceled) {
		t.Fatalf("load lost cancellation: %v", err)
	}
	if _, _, err := loaded.ReadResource(cancelled, "SKILL.md", 1024); !errors.Is(err, context.Canceled) {
		t.Fatalf("resource lost cancellation: %v", err)
	}
	if err := os.WriteFile(path, []byte("---\nname: pdf\ndescription: PDF\n---"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadRoot(ctx, root); err != nil {
		t.Fatalf("valid YAML closing delimiter at EOF rejected: %v", err)
	}
}

func TestCatalogEnforcesLimitsAndStringMetadata(t *testing.T) {
	for _, body := range []string{"---\nname: 123\ndescription: PDF\n---\ntext", "---\nname: pdf\nname: other\ndescription: PDF\n---\ntext"} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		c := NewCatalog(CatalogOptions{})
		if _, err := c.LoadRoot(context.Background(), root); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("invalid metadata accepted: %v", err)
		}
	}
	root := t.TempDir()
	fixture(t, root, "pdf", "PDF")
	c := NewCatalog(CatalogOptions{MaxFileBytes: 16})
	if _, err := c.LoadRoot(context.Background(), root); err == nil {
		t.Fatal("oversized file accepted")
	}
}

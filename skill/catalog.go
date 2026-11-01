// Package skill indexes local Skill instruction packages without exposing their
// contents to a model until an explicitly selected invocation.
package skill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Stable errors allow callers to distinguish repairable selection errors.
var (
	ErrNotFound          = errors.New("skill: not found")
	ErrInvalidMetadata   = errors.New("skill: invalid metadata")
	ErrDuplicateName     = errors.New("skill: duplicate name")
	ErrCatalogDrift      = errors.New("skill: catalog drift")
	ErrResourceDenied    = errors.New("skill: resource denied")
	ErrSelectionExpired  = errors.New("skill: selection expired")
	ErrSelectionMismatch = errors.New("skill: selection mismatch")
)

// CatalogOptions bounds filesystem work. Zero values select safe defaults.
type CatalogOptions struct {
	MaxFileBytes        int64
	MaxFrontMatterBytes int
	MaxSkills           int
	MaxDepth            int
}

// Metadata is an immutable value describing one local Skill.
type Metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Dir         string `json:"-"`
	SkillFile   string `json:"-"`
	Root        string `json:"-"`
	Fingerprint string `json:"fingerprint"`
}

// LoadReport describes an atomic root replacement.
type LoadReport struct {
	Added, Updated, Removed, Unchanged int
	Generation                         uint64
}

// Catalog owns metadata only; loading never retains the instruction body.
type Catalog struct {
	loadMu     sync.Mutex
	mu         sync.RWMutex
	options    CatalogOptions
	roots      map[string]map[string]Metadata
	entries    map[string]Metadata
	generation uint64
}

// NewCatalog creates an empty, concurrency-safe catalog.
func NewCatalog(options CatalogOptions) *Catalog {
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = 1 << 20
	}
	if options.MaxFrontMatterBytes <= 0 {
		options.MaxFrontMatterBytes = 64 << 10
	}
	if options.MaxSkills <= 0 {
		options.MaxSkills = 10000
	}
	if options.MaxDepth <= 0 {
		options.MaxDepth = 32
	}
	return &Catalog{options: options, roots: map[string]map[string]Metadata{}, entries: map[string]Metadata{}}
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func readBounded(ctx context.Context, path string, max int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("file size limit exceeded")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b, nil
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func parse(b []byte, maxFront int) (string, string, error) {
	if !utf8.Valid(b) {
		return "", "", ErrInvalidMetadata
	}
	normalized := bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return "", "", ErrInvalidMetadata
	}
	end := bytes.Index(normalized[4:], []byte("\n---\n"))
	if end < 0 && bytes.HasSuffix(normalized, []byte("\n---")) {
		end = len(normalized) - 4 - 4
	}
	if end < 0 || end > maxFront {
		return "", "", ErrInvalidMetadata
	}
	var node yaml.Node
	if err := yaml.Unmarshal(normalized[4:4+end], &node); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return "", "", ErrInvalidMetadata
	}
	fields := map[string]string{}
	content := node.Content[0].Content
	for i := 0; i < len(content); i += 2 {
		key, value := content[i], content[i+1]
		if key.Value != "name" && key.Value != "description" {
			continue
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
			return "", "", ErrInvalidMetadata
		}
		if _, ok := fields[key.Value]; ok {
			return "", "", ErrInvalidMetadata
		}
		fields[key.Value] = strings.TrimSpace(value.Value)
	}
	if fields["name"] == "" || fields["description"] == "" {
		return "", "", ErrInvalidMetadata
	}
	return fields["name"], fields["description"], nil
}

// LoadRoot atomically replaces one root. A failed scan preserves every old entry.
// Loading overlapping roots or duplicate logical names is rejected.
func (c *Catalog) LoadRoot(ctx context.Context, root string) (LoadReport, error) {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	absolute, err := filepath.Abs(root)
	if err != nil {
		return LoadReport{}, err
	}
	root, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return LoadReport{}, err
	}
	c.mu.RLock()
	for existing := range c.roots {
		if existing != root && (within(root, existing) || within(existing, root)) {
			c.mu.RUnlock()
			return LoadReport{}, fmt.Errorf("skill: overlapping roots")
		}
	}
	c.mu.RUnlock()
	next := map[string]Metadata{}
	var scan func(string, int) error
	scan = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > c.options.MaxDepth {
			return errors.New("skill: scan depth exceeded")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != "SKILL.md" {
				continue
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return ErrResourceDenied
			}
			path := filepath.Join(dir, "SKILL.md")
			b, err := readBounded(ctx, path, c.options.MaxFileBytes)
			if err != nil {
				return err
			}
			name, description, err := parse(b, c.options.MaxFrontMatterBytes)
			if err != nil {
				return fmt.Errorf("%w: %s", err, path)
			}
			if _, ok := next[name]; ok {
				return fmt.Errorf("%w: %s", ErrDuplicateName, name)
			}
			next[name] = Metadata{Name: name, Description: description, Dir: dir, SkillFile: path, Root: root, Fingerprint: digest(b)}
			if len(next) > c.options.MaxSkills {
				return errors.New("skill: root count exceeded")
			}
			return nil
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				if err := scan(filepath.Join(dir, entry.Name()), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := scan(root, 0); err != nil {
		return LoadReport{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for name := range next {
		if old, ok := c.entries[name]; ok && old.Root != root {
			return LoadReport{}, fmt.Errorf("%w: %s", ErrDuplicateName, name)
		}
	}
	report := LoadReport{}
	old := c.roots[root]
	for name, value := range next {
		previous, ok := old[name]
		if !ok {
			report.Added++
		} else if previous == value {
			report.Unchanged++
		} else {
			report.Updated++
		}
	}
	for name := range old {
		if _, ok := next[name]; !ok {
			report.Removed++
		}
		delete(c.entries, name)
	}
	for name, value := range next {
		c.entries[name] = value
	}
	c.roots[root] = next
	if report.Added+report.Updated+report.Removed > 0 {
		c.generation++
	}
	report.Generation = c.generation
	return report, nil
}

// Metadata returns a deterministic copy of the in-memory catalog.
func (c *Catalog) Metadata() []Metadata {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Metadata, 0, len(c.entries))
	for _, v := range c.entries {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Loaded pins the exact instruction bytes read during invocation.
type Loaded struct {
	Metadata
	Instructions string `json:"instructions"`
}

// Load reads only the requested Skill and verifies both the catalog and disk version.
func (c *Catalog) Load(ctx context.Context, name, fingerprint string) (Loaded, error) {
	c.mu.RLock()
	meta, ok := c.entries[name]
	c.mu.RUnlock()
	if !ok {
		return Loaded{}, ErrNotFound
	}
	if meta.Fingerprint != fingerprint {
		return Loaded{}, ErrCatalogDrift
	}
	path, err := filepath.EvalSymlinks(meta.SkillFile)
	if err != nil {
		return Loaded{}, ErrCatalogDrift
	}
	if path != meta.SkillFile {
		return Loaded{}, ErrResourceDenied
	}
	b, err := readBounded(ctx, path, c.options.MaxFileBytes)
	if err != nil {
		if ctx.Err() != nil {
			return Loaded{}, ctx.Err()
		}
		return Loaded{}, fmt.Errorf("%w: unreadable instructions", ErrCatalogDrift)
	}
	if digest(b) != fingerprint {
		return Loaded{}, ErrCatalogDrift
	}
	return Loaded{Metadata: meta, Instructions: string(b)}, nil
}

// ReadResource reads bounded UTF-8 text confined to this Skill directory.
func (l Loaded) ReadResource(ctx context.Context, path string, max int64) (string, string, error) {
	b, hash, err := l.ReadFile(ctx, path, max)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return "", "", fmt.Errorf("%w: resource is not text", ErrResourceDenied)
	}
	return string(b), hash, nil
}

// ReadFile reads bounded binary or text bytes confined to this Skill root.
// It supports explicit script/asset approval without relaxing text tool limits.
func (l Loaded) ReadFile(ctx context.Context, path string, max int64) ([]byte, string, error) {
	if path == "" || filepath.IsAbs(path) || !within(l.Dir, filepath.Join(l.Dir, path)) {
		return nil, "", ErrResourceDenied
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(l.Dir, path))
	if err != nil || !within(l.Dir, resolved) {
		return nil, "", ErrResourceDenied
	}
	b, err := readBounded(ctx, resolved, max)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		return nil, "", fmt.Errorf("%w: unreadable resource", ErrResourceDenied)
	}
	return b, digest(b), nil
}

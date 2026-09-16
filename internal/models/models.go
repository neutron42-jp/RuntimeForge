// Package models scans directories for GGUF model files, reads their
// metadata and groups split shards into a single logical model
// (SPEC §12).
package models

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"runtimeforge/internal/gguf"
)

// Model is a registered model file (or shard group).
type Model struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	MetaName     string    `json:"meta_name,omitempty"`
	Path         string    `json:"path"`
	Architecture string    `json:"architecture"`
	Quantization string    `json:"quantization"`
	SizeLabel    string    `json:"size_label,omitempty"`
	SizeBytes    int64     `json:"size_bytes"`
	Format       string    `json:"format"`
	ShardTotal   int       `json:"shard_total"`
	Shards       []string  `json:"shards,omitempty"`
	Projector    bool      `json:"projector,omitempty"`
	ModifiedAt   time.Time `json:"modified_at"`
	SourceDir    string    `json:"source_dir"`
	ggufFileType uint64
}

// Registry is a persisted list of models.
type Registry struct {
	path   string
	models []Model
}

// NewRegistry loads a registry from disk (empty when missing).
func NewRegistry(path string) (*Registry, error) {
	r := &Registry{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, err
	}
	var doc struct {
		Models []Model `json:"models"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse model registry: %w", err)
	}
	r.models = doc.Models
	// Name is derived from the file path. Recompute on load so registries
	// written by older versions (or from untrusted GGUF metadata) are
	// corrected without requiring a rescan.
	for i := range r.models {
		if r.models[i].Path != "" {
			r.models[i].Name = displayName(r.models[i].Path)
		}
	}
	return r, nil
}

// List returns the registered models sorted by name.
func (r *Registry) List() []Model {
	out := make([]Model, 0, len(r.models))
	out = append(out, r.models...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a model by id.
func (r *Registry) Get(id string) (Model, bool) {
	for _, m := range r.models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// Replace swaps the registry contents and persists them.
func (r *Registry) Replace(list []Model) error {
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	r.models = list
	return r.save()
}

func (r *Registry) save() error {
	doc := struct {
		Models []Model `json:"models"`
	}{Models: r.models}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// Dir is a directory to scan. Depth controls how many levels below the
// root are visited: -1 means unlimited, 0 means only files directly in
// the root.
type Dir struct {
	Path  string `json:"path"`
	Depth int    `json:"depth"`
}

// Scan walks the given directories to unlimited depth. It is a
// convenience wrapper around ScanTargets.
func Scan(dirs []string) ([]Model, []error) {
	targets := make([]Dir, 0, len(dirs))
	for _, d := range dirs {
		targets = append(targets, Dir{Path: d, Depth: -1})
	}
	return ScanTargets(targets, nil)
}

// ScanTargets walks the given directories (with per-directory depth) and
// explicit files, returning the discovered models. Files that fail to
// parse are reported in the returned error list but do not abort the
// scan.
func ScanTargets(dirs []Dir, files []string) ([]Model, []error) {
	groups := map[string]*group{}
	var errs []error

	addFile := func(path, sourceDir string) {
		shard, isShard := gguf.ParseShard(path)
		key := groupKey(path, shard, isShard)
		g := groups[key]
		if g == nil {
			g = &group{key: key, sourceDir: sourceDir}
			groups[key] = g
		}
		g.files = append(g.files, path)
		if isShard {
			g.shardTotal = shard.Total
		} else {
			g.shardTotal = 1
		}
		if !isShard || shard.Index == 1 {
			meta, err := readMeta(path)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", path, err))
				return
			}
			g.meta = meta
			g.primary = path
		}
	}

	for _, target := range dirs {
		root := expandHome(target.Path)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				errs = append(errs, err)
				return nil
			}
			if d.IsDir() {
				if path == root {
					return nil
				}
				if strings.HasPrefix(d.Name(), ".") {
					return filepath.SkipDir
				}
				if target.Depth >= 0 {
					rel, relErr := filepath.Rel(root, path)
					if relErr == nil {
						level := strings.Count(rel, string(os.PathSeparator)) + 1
						if level > target.Depth {
							return filepath.SkipDir
						}
					}
				}
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".gguf") {
				return nil
			}
			addFile(path, root)
			return nil
		})
		if walkErr != nil {
			errs = append(errs, walkErr)
		}
	}

	for _, f := range files {
		path := expandHome(f)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			errs = append(errs, fmt.Errorf("%s: not a readable file", path))
			continue
		}
		addFile(path, filepath.Dir(path))
	}

	var out []Model
	for _, g := range groups {
		if g.meta == nil || g.primary == "" {
			continue
		}
		sort.Strings(g.files)
		out = append(out, g.toModel())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, errs
}

type group struct {
	key        string
	sourceDir  string
	files      []string
	primary    string
	shardTotal int
	meta       *fileMeta
}

type fileMeta struct {
	file *gguf.File
	size int64
	mod  time.Time
	path string
}

func readMeta(path string) (*fileMeta, error) {
	f, err := gguf.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &fileMeta{file: f, size: info.Size(), mod: info.ModTime(), path: path}, nil
}

func (g *group) toModel() Model {
	var size int64
	for _, f := range g.files {
		if info, err := os.Stat(f); err == nil {
			size += info.Size()
		}
	}
	quant := gguf.QuantFromName(g.primary)
	if quant == "" {
		quant = g.meta.file.Quantization()
	}
	arch := g.meta.file.Architecture()
	return Model{
		ID:           modelID(g.primary),
		Name:         displayName(g.primary),
		MetaName:     g.meta.file.Name(),
		Path:         g.primary,
		Architecture: arch,
		Quantization: quant,
		SizeLabel:    g.meta.file.SizeLabel(),
		SizeBytes:    size,
		Format:       "gguf",
		ShardTotal:   g.shardTotal,
		Shards:       g.files,
		Projector:    arch == "clip",
		ModifiedAt:   g.meta.mod,
		SourceDir:    g.sourceDir,
		ggufFileType: g.meta.file.Uint(gguf.KeyFileType),
	}
}

// genericStems are file stems that carry no model identity.
var genericStems = map[string]bool{
	"mmproj": true, "model": true, "ggml-model": true,
	"consolidated": true, "checkpoint": true, "weights": true,
}

// displayName derives a human friendly name from the file path. Many
// GGUFs store a meaningless general.name (a hash, a training checkpoint
// id), so the curated file name is preferred and general.name is kept
// only as MetaName.
func displayName(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if shard, ok := gguf.ParseShard(path); ok {
		base = shard.Base
	}
	if q := gguf.QuantFromName(base + ".gguf"); q != "" {
		if idx := strings.LastIndex(strings.ToLower(base), strings.ToLower(q)); idx > 0 {
			base = strings.TrimRight(base[:idx], "-_. ")
		}
	}
	base = removeFold(base, "mmproj")
	base = strings.Trim(base, "-_. ")
	if base != "" && !genericStems[strings.ToLower(base)] {
		return base
	}
	// Fall back to the containing folder, which usually names the model.
	parent := filepath.Base(filepath.Dir(path))
	parent = strings.TrimSuffix(parent, "-GGUF")
	parent = strings.TrimSuffix(parent, "-gguf")
	parent = strings.TrimSpace(strings.Trim(parent, "-_. "))
	if parent != "" && parent != "." && parent != "/" {
		if base == "" {
			return parent
		}
		return parent + " (" + base + ")"
	}
	if base != "" {
		return base
	}
	return filepath.Base(path)
}

// removeFold removes every case-insensitive occurrence of sub.
func removeFold(s, sub string) string {
	lower := strings.ToLower(s)
	for {
		idx := strings.Index(lower, strings.ToLower(sub))
		if idx < 0 {
			return s
		}
		s = s[:idx] + s[idx+len(sub):]
		lower = strings.ToLower(s)
	}
}

func groupKey(path string, shard gguf.Shard, isShard bool) string {
	dir := filepath.Dir(path)
	if isShard {
		return filepath.Join(dir, shard.Base)
	}
	return filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".gguf"))
}

func modelID(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	sum := sha1.Sum([]byte(abs))
	return hex.EncodeToString(sum[:])[:12]
}

// ExpandHome resolves a leading "~" in a path.
func ExpandHome(p string) string { return expandHome(p) }

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

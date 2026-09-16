// Package runtimes records the artifacts produced by successful builds
// and extracts the model architectures each build supports (SPEC §9).
package runtimes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// llamaArchFile is the source file that lists supported architectures.
var llamaArchCandidates = []string{
	"src/llama-arch.cpp",
	"src/llama-arch.h",
}

var quoted = regexp.MustCompile(`"([^"]*)"`)

// ExtractArchitectures parses LLM_ARCH_NAMES from a llama.cpp source
// tree and returns the architecture names it supports. The result is
// sorted and de-duplicated; the "(unknown)" sentinel is dropped.
func ExtractArchitectures(srcDir string) ([]string, error) {
	var path string
	for _, cand := range llamaArchCandidates {
		p := filepath.Join(srcDir, cand)
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return nil, fmt.Errorf("no llama-arch source found under %s", srcDir)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, ok := extractMapBlock(string(data), "LLM_ARCH_NAMES")
	if !ok {
		return nil, errors.New("LLM_ARCH_NAMES map not found")
	}
	seen := map[string]bool{}
	var archs []string
	for _, m := range quoted.FindAllStringSubmatch(block, -1) {
		name := m[1]
		if name == "" || name == "(unknown)" || seen[name] {
			continue
		}
		seen[name] = true
		archs = append(archs, name)
	}
	if len(archs) == 0 {
		return nil, errors.New("no architectures parsed from LLM_ARCH_NAMES")
	}
	sort.Strings(archs)
	return archs, nil
}

// extractMapBlock returns the text of the initializer list following
// the named identifier, using brace matching.
func extractMapBlock(src, name string) (string, bool) {
	idx := strings.Index(src, name)
	if idx < 0 {
		return "", false
	}
	open := strings.IndexByte(src[idx:], '{')
	if open < 0 {
		return "", false
	}
	open += idx
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i], true
			}
		}
	}
	return "", false
}

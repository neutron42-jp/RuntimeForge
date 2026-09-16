package gguf

import (
	"path/filepath"
	"regexp"
	"strings"
)

// quantPattern matches quantization tokens as they appear in model file
// names (e.g. Q4_K_M, IQ4_XS, MXFP4_MOE, Q1_0). "general.file_type" is
// unreliable across llama.cpp versions, so the file name is the primary
// source for the displayed quantization.
var quantPattern = regexp.MustCompile(`(?i)(I?Q[0-9]+(?:_[0-9]+)*(?:_[A-Z0-9]+)*|BF16|F16|F32|MXFP4(?:_MOE)?|NVFP4|TQ[12]_0)`)

// QuantFromName extracts the quantization label from a file path. It
// returns "" when no token is found.
func QuantFromName(path string) string {
	name := filepath.Base(path)
	matches := quantPattern.FindAllString(name, -1)
	if len(matches) == 0 {
		return ""
	}
	return strings.ToUpper(matches[len(matches)-1])
}

package build

import (
	"fmt"
	"os"
	"strings"

	"runtimeforge/internal/config"
)

// LookPather resolves an executable name. The hardware.CommandRunner
// satisfies this.
type LookPather interface {
	LookPath(file string) (string, error)
}

// Preflight reports the tools missing for a build as human readable
// messages. An empty slice means the build can proceed.
func Preflight(cfg config.Config, backends []string, look LookPather) []string {
	var missing []string
	if look == nil {
		return missing
	}

	set := NormalizeBackends(backends)
	if len(set) == 0 {
		set = []string{"cpu"}
	}

	require := func(display, name string) {
		if name == "" {
			return
		}
		if _, err := look.LookPath(name); err != nil {
			missing = append(missing, fmt.Sprintf("%s not found (%s)", display, hint(name)))
		}
	}
	requirePath := func(display, path string) {
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			missing = append(missing, fmt.Sprintf("%s not found at %s", display, path))
		}
	}

	if cfg.Toolchain.CMake != "" {
		requirePath("cmake", cfg.Toolchain.CMake)
	} else {
		require("cmake", "cmake")
	}
	if strings.EqualFold(cfg.Build.Generator, "Ninja") {
		if cfg.Toolchain.Ninja != "" {
			requirePath("ninja", cfg.Toolchain.Ninja)
		} else {
			require("ninja", "ninja")
		}
	}
	require("git", "git")

	cc := cfg.Build.CC
	cxx := cfg.Build.CXX
	for _, b := range set {
		bc, ok := cfg.Build.Backends[b]
		if !ok {
			return append(missing, fmt.Sprintf("unknown backend %q", b))
		}
		if bc.CC != "" {
			cc = bc.CC
		}
		if bc.CXX != "" {
			cxx = bc.CXX
		}
	}
	require("C compiler", orDefault(cc, "cc"))
	require("C++ compiler", orDefault(cxx, "c++"))

	for _, b := range set {
		switch b {
		case "cuda":
			if cfg.Toolchain.NVCC != "" {
				requirePath("nvcc", cfg.Toolchain.NVCC)
			} else {
				require("nvcc", "nvcc")
			}
		case "vulkan":
			require("glslc", "glslc")
		}
	}
	return missing
}

func hint(tool string) string {
	switch tool {
	case "nvcc":
		return "install the CUDA toolkit"
	case "glslc":
		return "install shaderc / glslc"
	case "ninja":
		return "install ninja-build"
	case "cmake":
		return "install cmake"
	case "git":
		return "install git"
	default:
		return "install it or set an explicit path in the toolchain config"
	}
}

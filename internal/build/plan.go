// Package build turns a source checkout plus configuration into a
// build plan and executes it, producing a runtime variant (SPEC §7, §8).
//
// A runtime variant may enable several ggml backends (CUDA, Vulkan, CPU)
// in a single CMake configure — llama.cpp selects the device at runtime.
package build

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"runtimeforge/internal/config"
)

// Plan is the fully resolved description of a build. It is pure data so
// it can be inspected in the UI and unit tested.
type Plan struct {
	Source      string            `json:"source"`
	Commit      string            `json:"commit"`
	Backends    []string          `json:"backends"`
	Backend     string            `json:"backend"` // label, e.g. "cpu+cuda+vulkan"
	SourceDir   string            `json:"source_dir"`
	BuildDir    string            `json:"build_dir"`
	InstallDir  string            `json:"install_dir"`
	CC          string            `json:"cc,omitempty"`
	CXX         string            `json:"cxx,omitempty"`
	Configure   []string          `json:"configure"` // argv (without the cmake program)
	Build       []string          `json:"build"`     // argv (without the cmake program)
	Defines     map[string]string `json:"defines"`
	Targets     []string          `json:"targets"`
	Parallelism int               `json:"parallelism"`
}

// DefaultTargets are the binaries RuntimeForge needs from a build.
var DefaultTargets = []string{"llama-server", "llama-cli", "llama-bench"}

// NormalizeBackends de-duplicates and sorts a backend selection. CPU is
// not implied: an empty selection means CPU-only.
func NormalizeBackends(backends []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, b := range backends {
		b = strings.TrimSpace(strings.ToLower(b))
		if b == "" || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// Label joins a backend set into a canonical label.
func Label(backends []string) string {
	set := NormalizeBackends(backends)
	if len(set) == 0 {
		return "cpu"
	}
	return strings.Join(set, "+")
}

// BuildPlan merges the layered configuration into a concrete command
// plan. All values come from config so every argument is user
// overridable (SPEC §6).
func BuildPlan(cfg config.Config, backends []string, srcName, commit, sourceDir, buildDir, installDir string) (Plan, error) {
	set := NormalizeBackends(backends)
	if len(set) == 0 {
		set = []string{"cpu"}
	}

	cc := cfg.Build.CC
	cxx := cfg.Build.CXX
	defines := map[string]string{}
	for k, v := range cfg.Build.CMakeDefines {
		defines[k] = v
	}
	extraConfigure := append([]string(nil), cfg.Build.ExtraConfigureArgs...)
	extraBuild := append([]string(nil), cfg.Build.ExtraBuildArgs...)

	for _, b := range set {
		bc, ok := cfg.Build.Backends[b]
		if !ok {
			return Plan{}, fmt.Errorf("unknown backend %q", b)
		}
		if !bc.Enabled {
			return Plan{}, fmt.Errorf("backend %q is disabled", b)
		}
		if bc.CC != "" {
			cc = bc.CC
		}
		if bc.CXX != "" {
			cxx = bc.CXX
		}
		for k, v := range bc.CMakeDefines {
			defines[k] = v
		}
		extraConfigure = append(extraConfigure, bc.ExtraConfigureArgs...)
		extraBuild = append(extraBuild, bc.ExtraBuildArgs...)
	}

	// nvcc validates its host compiler separately from CC/CXX. Point it at
	// the configured C++ compiler so a newer system default (e.g. gcc 16
	// on Fedora 44, which nvcc rejects) is not picked up.
	if containsBackend(set, "cuda") {
		if _, ok := defines["CMAKE_CUDA_HOST_COMPILER"]; !ok && cxx != "" {
			defines["CMAKE_CUDA_HOST_COMPILER"] = cxx
		}
	}

	generator := orDefault(cfg.Build.Generator, "Ninja")
	buildType := orDefault(cfg.Build.BuildType, "Release")

	configure := []string{"-S", sourceDir, "-B", buildDir, "-G", generator}
	configure = append(configure, "-DCMAKE_BUILD_TYPE="+buildType)
	for _, k := range sortedKeys(defines) {
		configure = append(configure, "-D"+k+"="+defines[k])
	}
	configure = append(configure, extraConfigure...)

	parallel := cfg.Build.ParallelJobs
	build := []string{"--build", buildDir, "--config", buildType, "--target"}
	build = append(build, DefaultTargets...)
	if parallel > 0 {
		build = append(build, "--parallel", strconv.Itoa(parallel))
	}
	build = append(build, extraBuild...)

	return Plan{
		Source:      srcName,
		Commit:      commit,
		Backends:    set,
		Backend:     Label(set),
		SourceDir:   sourceDir,
		BuildDir:    buildDir,
		InstallDir:  installDir,
		CC:          cc,
		CXX:         cxx,
		Configure:   configure,
		Build:       build,
		Defines:     defines,
		Targets:     append([]string(nil), DefaultTargets...),
		Parallelism: parallel,
	}, nil
}

// Env returns the environment overrides for the plan.
func (p Plan) Env() []string {
	var env []string
	if p.CC != "" {
		env = append(env, "CC="+p.CC)
	}
	if p.CXX != "" {
		env = append(env, "CXX="+p.CXX)
	}
	return env
}

func containsBackend(set []string, name string) bool {
	for _, b := range set {
		if b == name {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

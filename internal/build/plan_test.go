package build

import (
	"reflect"
	"strings"
	"testing"

	"runtimeforge/internal/config"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	var c config.Config
	if err := config.Decode(config.Defaults(), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBuildPlanCUDA(t *testing.T) {
	cfg := testConfig(t)
	p, err := BuildPlan(cfg, []string{"cuda"}, "upstream", "abc123", "/src", "/build", "/install")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(p.Configure, " ")
	for _, want := range []string{"-S /src", "-B /build", "-G Ninja", "-DCMAKE_BUILD_TYPE=Release", "-DGGML_CUDA=ON", "-DGGML_NATIVE=ON", "-DCMAKE_INSTALL_RPATH=$ORIGIN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("configure missing %q: %v", want, p.Configure)
		}
	}
	if p.CC != "gcc-15" || p.CXX != "g++-15" {
		t.Errorf("compiler = %q/%q", p.CC, p.CXX)
	}
	if !reflect.DeepEqual(p.Env(), []string{"CC=gcc-15", "CXX=g++-15"}) {
		t.Errorf("env = %v", p.Env())
	}
	// Defines must be emitted sorted for reproducibility.
	var order []string
	for i, a := range p.Configure {
		if strings.HasPrefix(a, "-D") && i > 0 {
			order = append(order, a)
		}
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Errorf("defines not sorted: %v", order)
			break
		}
	}
	if p.BuildDir != "/build" || p.InstallDir != "/install" {
		t.Errorf("dirs = %q/%q", p.BuildDir, p.InstallDir)
	}
}

func TestBuildPlanCUDAHostCompiler(t *testing.T) {
	cfg := testConfig(t)
	p, err := BuildPlan(cfg, []string{"cuda"}, "s", "c", "/src", "/b", "/i")
	if err != nil {
		t.Fatal(err)
	}
	if p.Defines["CMAKE_CUDA_HOST_COMPILER"] != "g++-15" {
		t.Errorf("CMAKE_CUDA_HOST_COMPILER = %q, want g++-15", p.Defines["CMAKE_CUDA_HOST_COMPILER"])
	}
	// A user-provided value must win.
	cuda := cfg.Build.Backends["cuda"]
	cuda.CMakeDefines = map[string]string{"CMAKE_CUDA_HOST_COMPILER": "/opt/gcc/bin/g++"}
	cfg.Build.Backends["cuda"] = cuda
	p, _ = BuildPlan(cfg, []string{"cuda"}, "s", "c", "/src", "/b", "/i")
	if p.Defines["CMAKE_CUDA_HOST_COMPILER"] != "/opt/gcc/bin/g++" {
		t.Errorf("user override ignored: %q", p.Defines["CMAKE_CUDA_HOST_COMPILER"])
	}
	// Non-CUDA backends must not set it.
	p, _ = BuildPlan(cfg, []string{"cpu"}, "s", "c", "/src", "/b", "/i")
	if _, ok := p.Defines["CMAKE_CUDA_HOST_COMPILER"]; ok {
		t.Error("cpu build should not set CMAKE_CUDA_HOST_COMPILER")
	}
}

func TestBuildPlanCombinedBackends(t *testing.T) {
	cfg := testConfig(t)
	p, err := BuildPlan(cfg, []string{"cuda", "vulkan", "cpu"}, "upstream", "abc", "/src", "/build", "/install")
	if err != nil {
		t.Fatal(err)
	}
	if p.Backend != "cpu+cuda+vulkan" {
		t.Errorf("label = %q, want cpu+cuda+vulkan", p.Backend)
	}
	if !reflect.DeepEqual(p.Backends, []string{"cpu", "cuda", "vulkan"}) {
		t.Errorf("backends = %v", p.Backends)
	}
	if p.Defines["GGML_CUDA"] != "ON" || p.Defines["GGML_VULKAN"] != "ON" {
		t.Errorf("combined defines missing: %v", p.Defines)
	}
	if p.Defines["CMAKE_CUDA_HOST_COMPILER"] != "g++-15" {
		t.Errorf("cuda host compiler not set in combined build")
	}
	// Exactly one configure invocation (one -S).
	if n := strings.Count(strings.Join(p.Configure, " "), "-S "); n != 1 {
		t.Errorf("expected a single configure step, got %d: %v", n, p.Configure)
	}
}

func TestBuildPlanGeneratorArgs(t *testing.T) {
	cfg := testConfig(t)
	p, _ := BuildPlan(cfg, []string{"cpu"}, "s", "c", "/src", "/build", "/install")
	// -G Ninja must be two separate argv entries.
	found := false
	for i := 0; i+1 < len(p.Configure); i++ {
		if p.Configure[i] == "-G" && p.Configure[i+1] == "Ninja" {
			found = true
		}
	}
	if !found {
		t.Errorf("generator argv missing: %v", p.Configure)
	}
}

func TestBuildPlanBackendOverrideCompiler(t *testing.T) {
	cfg := testConfig(t)
	cuda := cfg.Build.Backends["cuda"]
	cuda.CC = "clang"
	cuda.CXX = "clang++"
	cfg.Build.Backends["cuda"] = cuda
	p, err := BuildPlan(cfg, []string{"cuda"}, "s", "c", "/src", "/b", "/i")
	if err != nil {
		t.Fatal(err)
	}
	if p.CC != "clang" || p.CXX != "clang++" {
		t.Errorf("backend override ignored: %q/%q", p.CC, p.CXX)
	}
}

func TestBuildPlanExtraArgsOrder(t *testing.T) {
	cfg := testConfig(t)
	cfg.Build.ExtraConfigureArgs = []string{"--global-flag"}
	cuda := cfg.Build.Backends["cuda"]
	cuda.ExtraConfigureArgs = []string{"--cuda-flag"}
	cuda.ExtraBuildArgs = []string{"--build-flag"}
	cfg.Build.Backends["cuda"] = cuda

	p, err := BuildPlan(cfg, []string{"cuda"}, "s", "c", "/src", "/b", "/i")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Configure, " ")
	if !strings.HasSuffix(got, "--global-flag --cuda-flag") {
		t.Errorf("extra configure args order: %v", p.Configure)
	}
	buildJoined := strings.Join(p.Build, " ")
	if !strings.Contains(buildJoined, "--target llama-server llama-cli llama-bench") {
		t.Errorf("targets = %v", p.Build)
	}
	if !strings.HasSuffix(buildJoined, "--build-flag") {
		t.Errorf("extra build args: %v", p.Build)
	}
}

func TestBuildPlanParallel(t *testing.T) {
	cfg := testConfig(t)
	cfg.Build.ParallelJobs = 4
	p, _ := BuildPlan(cfg, []string{"cpu"}, "s", "c", "/src", "/b", "/i")
	joined := strings.Join(p.Build, " ")
	if !strings.Contains(joined, "--parallel 4") {
		t.Errorf("parallel flag missing: %v", p.Build)
	}
	cfg.Build.ParallelJobs = 0
	p, _ = BuildPlan(cfg, []string{"cpu"}, "s", "c", "/src", "/b", "/i")
	if strings.Contains(strings.Join(p.Build, " "), "--parallel") {
		t.Errorf("parallel flag should be omitted when 0: %v", p.Build)
	}
}

func TestBuildPlanErrors(t *testing.T) {
	cfg := testConfig(t)
	if _, err := BuildPlan(cfg, []string{"nope"}, "s", "c", "/s", "/b", "/i"); err == nil {
		t.Error("unknown backend should error")
	}
	cpu := cfg.Build.Backends["cpu"]
	cpu.Enabled = false
	cfg.Build.Backends["cpu"] = cpu
	if _, err := BuildPlan(cfg, []string{"cpu"}, "s", "c", "/s", "/b", "/i"); err == nil {
		t.Error("disabled backend should error")
	}
}

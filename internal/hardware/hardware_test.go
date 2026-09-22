package hardware

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	paths   map[string]string
	outputs map[string]string
	fail    map[string]bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		paths:   map[string]string{},
		outputs: map[string]string{},
		fail:    map[string]bool{},
	}
}

func (f *fakeRunner) add(name, version string) *fakeRunner {
	f.paths[name] = "/usr/bin/" + name
	f.outputs[name+" --version"] = version + "\n"
	return f
}

func (f *fakeRunner) put(key, out string) *fakeRunner {
	f.outputs[key] = out
	return f
}

func (f *fakeRunner) LookPath(file string) (string, error) {
	if p, ok := f.paths[file]; ok {
		return p, nil
	}
	return "", errors.New("not found")
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	key := filepath.Base(name) + " " + strings.Join(args, " ")
	if out, ok := f.outputs[key]; ok {
		if f.fail[key] {
			return []byte(out), errors.New("exit status 1")
		}
		return []byte(out), nil
	}
	return nil, errors.New("no such output: " + key)
}

const sampleVulkan = `
==========
VULKANINFO
==========
Devices:
========
GPU0:
	apiVersion         = 1.4.354
	deviceName         = AMD Radeon 890M Graphics (RADV STRIX1)
	driverName         = radv
	driverInfo         = Mesa 26.1.8
GPU1:
	apiVersion         = 1.4.341
	deviceName         = NVIDIA GeForce RTX 4090
	driverName         = NVIDIA
GPU2:
	apiVersion         = 1.4.354
	deviceName         = llvmpipe (LLVM 22.1.8, 256 bits)
	driverName         = llvmpipe
`

const sampleSMI = "NVIDIA GeForce RTX 4090, 24564, 555.42.02\n"

func fullRunner() *fakeRunner {
	f := newFakeRunner()
	f.add("cmake", "cmake version 4.3.0")
	f.add("ninja", "1.13.2")
	f.add("git", "git version 2.55.0")
	f.add("gcc-15", "gcc-15 (GCC) 15.3.1")
	f.add("g++-15", "g++-15 (GCC) 15.3.1")
	f.add("nvcc", "nvcc: NVIDIA (R) Cuda compiler")
	f.add("vulkaninfo", "vulkaninfo summary")
	f.add("glslc", "shaderc v2026.1")
	f.add("nvidia-smi", "NVIDIA-SMI 555.42.02")
	f.put("vulkaninfo --summary", sampleVulkan)
	f.put("nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader,nounits", sampleSMI)
	return f
}

func TestDetectFullHost(t *testing.T) {
	rep := Detect(context.Background(), fullRunner())

	if !rep.HasBackend("cuda") {
		t.Errorf("cuda missing: backends=%v", rep.Backends)
	}
	if !rep.HasBackend("vulkan") {
		t.Errorf("vulkan missing: backends=%v", rep.Backends)
	}
	if !rep.HasBackend("cpu") {
		t.Errorf("cpu missing: backends=%v", rep.Backends)
	}
	if got := rep.Tool("cc").Path; got != "/usr/bin/gcc-15" {
		t.Errorf("cc path = %q, want gcc-15 preferred", got)
	}
	if rep.Tool("nvcc").Version == "" {
		t.Errorf("nvcc version not parsed")
	}

	// Vulkan NVIDIA device should merge into the nvidia-smi GPU.
	var nvidia *GPU
	for i := range rep.GPUs {
		if rep.GPUs[i].Vendor == "nvidia" {
			nvidia = &rep.GPUs[i]
		}
	}
	if nvidia == nil {
		t.Fatal("nvidia GPU not found")
	}
	if nvidia.MemoryMB != 24564 {
		t.Errorf("nvidia memory = %d, want 24564 (from nvidia-smi)", nvidia.MemoryMB)
	}
	if !hasAPI(nvidia.APIs, "vulkan") || !hasAPI(nvidia.APIs, "cuda") {
		t.Errorf("nvidia APIs = %v, want cuda+vulkan", nvidia.APIs)
	}

	// AMD iGPU should be its own entry.
	foundAMD := false
	for _, g := range rep.GPUs {
		if g.Vendor == "amd" {
			foundAMD = true
		}
	}
	if !foundAMD {
		t.Errorf("AMD GPU not detected: %+v", rep.GPUs)
	}
}

func TestDetectSoftwareVulkanOnly(t *testing.T) {
	f := newFakeRunner()
	f.add("vulkaninfo", "vulkaninfo summary")
	f.add("glslc", "shaderc")
	f.put("vulkaninfo --summary", `
GPU0:
	deviceName         = llvmpipe (LLVM 22.1.8, 256 bits)
	driverName         = llvmpipe
`)
	rep := Detect(context.Background(), f)
	if rep.HasBackend("vulkan") {
		t.Errorf("software vulkan must not enable the vulkan backend: %v", rep.Backends)
	}
	if !rep.HasBackend("cpu") {
		t.Errorf("cpu must always be available")
	}
}

func TestDetectNoSMI(t *testing.T) {
	f := newFakeRunner()
	f.add("vulkaninfo", "v")
	f.put("vulkaninfo --summary", `
GPU0:
	deviceName         = AMD Radeon 890M Graphics (RADV STRIX1)
	driverName         = radv
`)
	rep := Detect(context.Background(), f)
	if rep.HasBackend("cuda") {
		t.Errorf("cuda must require nvidia-smi: %v", rep.Backends)
	}
	if !rep.HasBackend("vulkan") {
		t.Errorf("vulkan should be available")
	}
}

func TestDetectEmptyRunner(t *testing.T) {
	rep := Detect(context.Background(), newFakeRunner())
	if len(rep.GPUs) != 0 {
		t.Errorf("expected no GPUs, got %+v", rep.GPUs)
	}
	if len(rep.Backends) != 1 || rep.Backends[0] != "cpu" {
		t.Errorf("backends = %v, want [cpu]", rep.Backends)
	}
	if rep.Tool("cmake").Found {
		t.Errorf("cmake should not be found")
	}
}

func hasAPI(apis []string, api string) bool {
	for _, a := range apis {
		if a == api {
			return true
		}
	}
	return false
}

func TestLookPathFallsBackToCUDAHome(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "rf-test-tool")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUDA_HOME", home)
	t.Setenv("CUDA_PATH", "")

	got, err := LookPath("rf-test-tool")
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if got != exe {
		t.Errorf("path = %q, want %q", got, exe)
	}
}

func TestLookPathMissing(t *testing.T) {
	t.Setenv("CUDA_HOME", t.TempDir())
	t.Setenv("CUDA_PATH", "")
	if _, err := LookPath("rf-definitely-missing-tool"); err == nil {
		t.Error("expected an error for a missing tool")
	}
}

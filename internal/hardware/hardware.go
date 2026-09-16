// Package hardware detects GPUs and the build toolchain available on
// the host. Detection is read-only and never touches the model runtime
// (SPEC §10).
package hardware

import (
	"context"
	"strings"
	"time"
)

// GPU describes a detected graphics device.
type GPU struct {
	Vendor   string   `json:"vendor"` // nvidia, amd, intel, software, unknown
	Name     string   `json:"name"`
	Driver   string   `json:"driver,omitempty"`
	MemoryMB int      `json:"memory_mb,omitempty"`
	APIs     []string `json:"apis"` // cuda, vulkan
}

// Tool describes an external executable used for building or detecting.
type Tool struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Version  string `json:"version,omitempty"`
	Found    bool   `json:"found"`
	Required bool   `json:"required,omitempty"`
}

// Report is the result of a detection pass.
type Report struct {
	DetectedAt time.Time       `json:"detected_at"`
	GPUs       []GPU           `json:"gpus"`
	Backends   []string        `json:"backends"`
	Tools      map[string]Tool `json:"tools"`
	Notes      []string        `json:"notes,omitempty"`
}

// HasBackend reports whether the named backend is available on the host.
func (r Report) HasBackend(name string) bool {
	for _, b := range r.Backends {
		if b == name {
			return true
		}
	}
	return false
}

// Tool returns the inventory entry for key.
func (r Report) Tool(key string) Tool { return r.Tools[key] }

type toolSpec struct {
	key        string
	candidates []string
	version    []string
}

var toolSpecs = []toolSpec{
	{"cmake", []string{"cmake"}, []string{"--version"}},
	{"ninja", []string{"ninja"}, []string{"--version"}},
	{"git", []string{"git"}, []string{"--version"}},
	{"cc", []string{"gcc-15", "gcc", "cc"}, []string{"--version"}},
	{"cxx", []string{"g++-15", "g++", "c++"}, []string{"--version"}},
	{"nvcc", []string{"nvcc"}, []string{"--version"}},
	{"vulkaninfo", []string{"vulkaninfo"}, []string{"--summary"}},
	{"glslc", []string{"glslc"}, []string{"--version"}},
	{"nvidia-smi", []string{"nvidia-smi"}, []string{"--version"}},
}

// Detect inspects the host. It never fails: unavailable information is
// recorded in Report.Notes so the UI can surface it.
func Detect(ctx context.Context, runner CommandRunner) Report {
	if runner == nil {
		runner = ExecRunner{}
	}
	rep := Report{DetectedAt: time.Now().UTC(), Tools: map[string]Tool{}}

	detectTools(ctx, runner, &rep)
	detectGPUs(ctx, runner, &rep)
	rep.Backends = availableBackends(rep)
	return rep
}

func detectTools(ctx context.Context, runner CommandRunner, rep *Report) {
	for _, spec := range toolSpecs {
		tool := Tool{Name: spec.key}
		for _, cand := range spec.candidates {
			path, err := runner.LookPath(cand)
			if err != nil {
				continue
			}
			tool.Found = true
			tool.Path = path
			tool.Name = spec.key
			if len(spec.version) > 0 {
				if out, err := runner.Run(ctx, path, spec.version...); err == nil {
					tool.Version = firstLine(string(out))
				}
			}
			break
		}
		rep.Tools[spec.key] = tool
	}
}

func detectGPUs(ctx context.Context, runner CommandRunner, rep *Report) {
	var gpus []GPU

	if tool, ok := rep.Tools["nvidia-smi"]; ok && tool.Found {
		out, err := runner.Run(ctx, tool.Path,
			"--query-gpu=name,memory.total,driver_version", "--format=csv,noheader,nounits")
		if err != nil && len(out) == 0 {
			rep.Notes = append(rep.Notes, "nvidia-smi query failed: "+err.Error())
		} else {
			gpus = append(gpus, parseNvidiaSMI(string(out))...)
		}
	}

	if tool, ok := rep.Tools["vulkaninfo"]; ok && tool.Found {
		out, err := runner.Run(ctx, tool.Path, "--summary")
		if err != nil && len(out) == 0 {
			rep.Notes = append(rep.Notes, "vulkaninfo failed: "+err.Error())
		} else {
			gpus = mergeGPUs(gpus, parseVulkanSummary(string(out)))
		}
	}

	rep.GPUs = gpus
}

// parseNvidiaSMI parses CSV lines: "name, memory.total, driver_version".
func parseNvidiaSMI(out string) []GPU {
	var gpus []GPU
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		g := GPU{Vendor: "nvidia", Name: fields[0], APIs: []string{"cuda"}}
		if len(fields) >= 2 {
			g.MemoryMB = atoiSafe(fields[1])
		}
		if len(fields) >= 3 {
			g.Driver = fields[2]
		}
		gpus = append(gpus, g)
	}
	return gpus
}

// parseVulkanSummary parses the device blocks of `vulkaninfo --summary`.
func parseVulkanSummary(out string) []GPU {
	var gpus []GPU
	var cur *GPU
	flush := func() {
		if cur != nil && cur.Name != "" {
			if len(cur.APIs) == 0 {
				cur.APIs = []string{"vulkan"}
			}
			gpus = append(gpus, *cur)
		}
		cur = nil
	}
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := splitKV(line)
		if !ok {
			continue
		}
		switch key {
		case "deviceName":
			flush()
			cur = &GPU{Name: val, APIs: []string{"vulkan"}}
		case "driverName":
			if cur != nil {
				cur.Driver = val
				cur.Vendor = vendorFromDriver(val)
			}
		}
	}
	flush()
	return gpus
}

func vendorFromDriver(driver string) string {
	switch strings.ToLower(driver) {
	case "nvidia":
		return "nvidia"
	case "radv", "amdvlk", "amd", "amdgpu-pro":
		return "amd"
	case "intel", "anv", "intel open source technology center":
		return "intel"
	case "llvmpipe", "lavapipe", "swiftshader":
		return "software"
	default:
		return "unknown"
	}
}

// mergeGPUs folds Vulkan devices into the NVIDIA list by name, adding
// the vulkan API rather than duplicating the device.
func mergeGPUs(base, extra []GPU) []GPU {
	for _, e := range extra {
		merged := false
		for i := range base {
			if strings.EqualFold(base[i].Name, e.Name) {
				base[i].APIs = addAPI(base[i].APIs, "vulkan")
				if base[i].Driver == "" {
					base[i].Driver = e.Driver
				}
				merged = true
				break
			}
		}
		if !merged {
			base = append(base, e)
		}
	}
	return base
}

func addAPI(apis []string, api string) []string {
	for _, a := range apis {
		if a == api {
			return apis
		}
	}
	return append(apis, api)
}

func availableBackends(rep Report) []string {
	backends := []string{}
	hasNvidia := false
	hasRealVulkan := false
	for _, g := range rep.GPUs {
		if g.Vendor == "nvidia" {
			hasNvidia = true
		}
		if g.Vendor != "software" {
			for _, a := range g.APIs {
				if a == "vulkan" {
					hasRealVulkan = true
				}
			}
		}
	}
	if hasNvidia && rep.Tools["nvidia-smi"].Found {
		backends = append(backends, "cuda")
	}
	if hasRealVulkan && rep.Tools["vulkaninfo"].Found {
		backends = append(backends, "vulkan")
	}
	backends = append(backends, "cpu")
	return backends
}

func splitKV(line string) (key, val string, ok bool) {
	idx := strings.Index(line, "=")
	if idx < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
}

func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

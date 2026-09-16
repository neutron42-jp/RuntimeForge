package config

// DefaultTOML is the built-in configuration. It is written to
// config.toml on first run and serves as the base of the override
// chain described in SPEC §6.
const DefaultTOML = `
[server]
host = "127.0.0.1"
port = 1234
internal_port_range = [20000, 20999]

[runtime]
backend_priority = ["cuda", "vulkan", "cpu"]
max_concurrent_builds = 1
auto_select = true

[build]
generator = "Ninja"
build_type = "Release"
parallel_jobs = 0
# Fedora 44 ships gcc 16 as the default, which llama.cpp fails to build
# with; pin gcc 15. Override with "" to use the system default compiler.
cc = "gcc-15"
cxx = "g++-15"
cmake_defines = { GGML_NATIVE = "ON", LLAMA_BUILD_TESTS = "OFF", LLAMA_BUILD_EXAMPLES = "OFF", CMAKE_BUILD_WITH_INSTALL_RPATH = "ON", CMAKE_INSTALL_RPATH = "$ORIGIN" }
extra_configure_args = []
extra_build_args = []

[build.backends.cuda]
enabled = true
cc = ""
cxx = ""
cmake_defines = { GGML_CUDA = "ON" }
extra_configure_args = []
extra_build_args = []

[build.backends.vulkan]
enabled = true
cc = ""
cxx = ""
cmake_defines = { GGML_VULKAN = "ON" }
extra_configure_args = []
extra_build_args = []

[build.backends.cpu]
enabled = true
cc = ""
cxx = ""
cmake_defines = {}
extra_configure_args = []
extra_build_args = []

[load]
extra_args = []
gpu_layers = ""
threads = 0
context_size = 0

[models]
scan_dirs = []
# How deep to recurse below each scan dir (-1 = unlimited).
scan_depth = -1
# Extra roots with a per-root depth, e.g.
#   roots = [{ path = "/data/models", depth = 3 }]
roots = []
# Individual .gguf files to register directly.
files = []

[toolchain]
cmake = ""
ninja = ""
nvcc = ""
skip_detect = false
`

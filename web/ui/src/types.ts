export interface Source {
  name: string
  url: string
  ref?: string
  local_commit?: string
  remote_commit?: string
  update_available: boolean
  last_checked_at?: string
  last_error?: string
}

export interface Manifest {
  id: string
  source: string
  git_url: string
  ref?: string
  commit: string
  backend: string
  built_at: string
  resolved_cmake_flags?: string[]
  env?: string[]
  binaries: Record<string, string>
  supported_architectures: string[]
  host_gpus?: string[]
}

export interface Model {
  id: string
  name: string
  meta_name?: string
  path: string
  architecture: string
  quantization: string
  size_label?: string
  size_bytes: number
  format: string
  shard_total: number
  shards?: string[]
  projector?: boolean
  modified_at: string
}

export interface ModelRoot {
  path: string
  depth: number
}

export interface ModelSources {
  scan_dirs: string[] | null
  scan_depth: number
  roots: ModelRoot[] | null
  files: string[] | null
}

export interface ModelSettings {
  context_size?: number
  gpu_layers?: string
  threads?: number
  cpu_range?: string
  eval_batch_size?: number
  flash_attn?: string
  cache_type_k?: string
  cache_type_v?: string
  kv_cache_offload?: string
  load_mode?: string
  seed?: number
  rope_freq_base?: string
  rope_freq_scale?: string
  extra_args?: string[]
}

export interface Instance {
  id: string
  model_id: string
  model_name: string
  model_path: string
  architecture: string
  runtime_id: string
  backend: string
  port: number
  pid: number
  status: string
  started_at: string
  ready_at?: string
  error?: string
  args: string[]
  tail?: string[]
}

export interface Job {
  id: string
  source: string
  commit: string
  backends?: string[]
  backend: string
  status: string
  queued_at: string
  started_at?: string
  finished_at?: string
  exit_code: number
  error?: string
  install_dir?: string
  log_path?: string
  plan?: { configure: string[]; build: string[]; [k: string]: unknown }
  tail?: string[]
}

export interface Selections {
  formats: Record<string, string>
  models: Record<string, string>
}

export interface GPU {
  vendor: string
  name: string
  driver?: string
  memory_mb?: number
  apis: string[]
}

export interface Tool {
  name: string
  path: string
  version?: string
  found: boolean
}

export interface Hardware {
  detected_at: string
  gpus: GPU[] | null
  backends: string[] | null
  tools: Record<string, Tool>
}

export interface SystemInfo {
  version: string
  root: string
  hardware: Hardware
}

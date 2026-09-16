import type {
  Hardware,
  Instance,
  Job,
  Manifest,
  Model,
  ModelRoot,
  ModelSources,
  Selections,
  Source,
  SystemInfo,
} from './types'

export interface Config {
  [key: string]: unknown
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const data = await res.json()
      msg = data?.error?.message ?? msg
    } catch {
      /* ignore */
    }
    throw new Error(msg)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
  system: () => req<SystemInfo>('GET', '/api/v1/system'),
  config: () => req<Config>('GET', '/api/v1/config'),
  updateConfig: (body: unknown) => req<Config>('PUT', '/api/v1/config', body),

  sources: () => req<Source[]>('GET', '/api/v1/sources'),
  addSource: (body: { name?: string; url: string; ref?: string }) =>
    req<Source>('POST', '/api/v1/sources', body),
  removeSource: (name: string) =>
    req<void>('DELETE', `/api/v1/sources/${encodeURIComponent(name)}`),
  checkSource: (name: string) =>
    req<Source>('POST', `/api/v1/sources/${encodeURIComponent(name)}/check`),
  build: (
    name: string,
    body: {
      backend?: string
      backends?: string[]
      all?: boolean
      commit?: string
      cmake_defines?: Record<string, string>
      extra_configure_args?: string[]
      extra_build_args?: string[]
      cc?: string
      cxx?: string
      generator?: string
      build_type?: string
      parallel_jobs?: number
    },
  ) => req<{ jobs: Job[] }>('POST', `/api/v1/sources/${encodeURIComponent(name)}/build`, body),

  builds: () => req<Job[]>('GET', '/api/v1/builds'),
  buildGet: (id: string) => req<Job>('GET', `/api/v1/builds/${encodeURIComponent(id)}`),
  cancelBuild: (id: string) => req<void>('POST', `/api/v1/builds/${encodeURIComponent(id)}/cancel`),

  runtimes: () => req<Manifest[]>('GET', '/api/v1/runtimes'),
  removeRuntime: (id: string) => req<void>('DELETE', `/api/v1/runtimes/${id}`),

  selections: () => req<Selections>('GET', '/api/v1/selections'),
  setSelection: (body: { format?: string; model?: string; runtime_id: string }) =>
    req<Selections>('PUT', '/api/v1/selections', body),

  models: () => req<Model[]>('GET', '/api/v1/models'),
  modelSources: () => req<ModelSources>('GET', '/api/v1/models/sources'),
  setModelSources: (body: {
    scan_dirs?: string[]
    scan_depth?: number
    roots: ModelRoot[]
    files: string[]
  }) =>
    req<{ count: number; errors: string[] | null; sources: ModelSources }>(
      'PUT',
      '/api/v1/models/sources',
      body,
    ),
  scanModels: () =>
    req<{ count: number; errors: string[] | null }>('POST', '/api/v1/models/scan'),
  loadModel: (
    id: string,
    body: { runtime_id?: string; context_size?: number; gpu_layers?: string; threads?: number; extra_args?: string[] },
  ) => req<Instance>('POST', `/api/v1/models/${encodeURIComponent(id)}/load`, body),
  unloadModel: (id: string) =>
    req<void>('POST', `/api/v1/models/${encodeURIComponent(id)}/unload`),

  instances: () => req<Instance[]>('GET', '/api/v1/instances'),
}

export function openEvents(onEvent: (type: string, data: unknown) => void): EventSource {
  const es = new EventSource('/api/v1/events')
  es.onmessage = (ev) => {
    try {
      const parsed = JSON.parse(ev.data)
      onEvent(parsed.type, parsed.data)
    } catch {
      /* ignore malformed frames */
    }
  }
  return es
}

export function formatBytes(n: number): string {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${units[i]}`
}

export type { Hardware }

import { Component, Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { api, formatBytes, openEvents } from './api'
import type {
  Arg,
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

type Tab = 'dashboard' | 'models' | 'server' | 'runtimes' | 'sources' | 'settings'

const TABS: { id: Tab; label: string }[] = [
  { id: 'dashboard', label: 'Dashboard' },
  { id: 'models', label: 'Models' },
  { id: 'server', label: 'Server' },
  { id: 'runtimes', label: 'Runtimes' },
  { id: 'sources', label: 'Sources' },
  { id: 'settings', label: 'Settings' },
]

export default function App() {
  const [tab, setTab] = useState<Tab>('dashboard')
  const [system, setSystem] = useState<SystemInfo | null>(null)
  const [sources, setSources] = useState<Source[]>([])
  const [runtimes, setRuntimes] = useState<Manifest[]>([])
  const [models, setModels] = useState<Model[]>([])
  const [instances, setInstances] = useState<Instance[]>([])
  const [builds, setBuilds] = useState<Job[]>([])
  const [selections, setSelections] = useState<Selections>({ formats: {}, models: {} })
  const [config, setConfig] = useState<unknown>(null)
  const [logs, setLogs] = useState<Record<string, string[]>>({})
  const [serverLogs, setServerLogs] = useState<Record<string, string[]>>({})
  const [restarting, setRestarting] = useState(false)
  const [toast, setToast] = useState<{ msg: string; error?: boolean } | null>(null)

  const notify = useCallback((msg: string, error = false) => {
    setToast({ msg, error })
    window.setTimeout(() => setToast(null), 5000)
  }, [])

  const refresh = useCallback(async () => {
    const settled = await Promise.allSettled([
      api.system().then(setSystem),
      api.sources().then(setSources),
      api.runtimes().then(setRuntimes),
      api.models().then(setModels),
      api.instances().then(setInstances),
      api.builds().then(setBuilds),
      api.selections().then(setSelections),
      api.config().then(setConfig),
    ])
    const failed = settled.find((r) => r.status === 'rejected')
    if (failed && failed.status === 'rejected') {
      notify(String((failed as PromiseRejectedResult).reason), true)
    }
  }, [notify])

  const refreshEnvironment = useCallback(async () => {
    try {
      await api.refreshEnvironment()
      notify('Environment re-detected')
      void refresh()
    } catch (e) {
      notify(String(e), true)
    }
  }, [notify, refresh])

  const restartService = useCallback(async () => {
    if (restarting) return
    if (!window.confirm('Restart RuntimeForge? Loaded models and running builds will be stopped.')) {
      return
    }
    setRestarting(true)
    try {
      await api.restartService()
    } catch (e) {
      setRestarting(false)
      notify(String(e), true)
      return
    }
    notify('Restarting RuntimeForge…')
    const deadline = Date.now() + 60000
    const timer = window.setInterval(async () => {
      try {
        await api.health()
        window.clearInterval(timer)
        setRestarting(false)
        notify('RuntimeForge restarted')
        void refresh()
      } catch {
        if (Date.now() > deadline) {
          window.clearInterval(timer)
          setRestarting(false)
          notify('RuntimeForge did not come back after the restart', true)
        }
      }
    }, 1000)
  }, [notify, refresh, restarting])

  useEffect(() => {
    void refresh()
  }, [refresh])

  useEffect(() => {
    const es = openEvents((type, data) => {
      if (type === 'log') {
        const d = data as { job_id: string; line: string }
        setLogs((prev) => {
          const next = { ...prev }
          const arr = (next[d.job_id] ?? []).concat(d.line)
          next[d.job_id] = arr.slice(-500)
          return next
        })
        return
      }
      if (type === 'server-log') {
        const d = data as { instance_id: string; line: string }
        setServerLogs((prev) => {
          const next = { ...prev }
          const arr = (next[d.instance_id] ?? []).concat(d.line)
          next[d.instance_id] = arr.slice(-500)
          return next
        })
        return
      }
      void refresh()
    })
    return () => es.close()
  }, [refresh])

  // Seed inference logs from the supervisor's retained tail the first
  // time an instance is seen, so history survives a page reload.
  useEffect(() => {
    setServerLogs((prev) => {
      let changed = false
      const next = { ...prev }
      for (const i of instances) {
        if (next[i.id] === undefined) {
          next[i.id] = i.tail ?? []
          changed = true
        }
      }
      return changed ? next : prev
    })
  }, [instances])

  const openaiURL = useMemo(() => `${window.location.origin}/v1`, [])

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand">
          Runtime<span>Forge</span>
        </div>
        {TABS.map((t) => (
          <div
            key={t.id}
            className={`nav-item ${tab === t.id ? 'active' : ''}`}
            onClick={() => setTab(t.id)}
          >
            {t.label}
          </div>
        ))}
        <div className="spacer" />
        <div className="muted mono" style={{ padding: '8px 12px', fontSize: 11 }}>
          {system?.version ?? ''}
        </div>
      </aside>

      <main className="main">
        <ErrorBoundary key={tab}>
        {tab === 'dashboard' && (
          <Dashboard system={system} instances={instances} builds={builds} openaiURL={openaiURL} restarting={restarting} onRefreshEnv={refreshEnvironment} onRestart={restartService} onUnload={async (id) => {
            try {
              await api.unloadModel(id)
              notify(`Unloaded ${id}`)
              void refresh()
            } catch (e) {
              notify(String(e), true)
            }
          }} />
        )}
        {tab === 'models' && (
          <ModelsTab
            models={models}
            instances={instances}
            runtimes={runtimes}
            selections={selections}
            onRefresh={refresh}
            notify={notify}
          />
        )}
        {tab === 'server' && (
          <ServerTab
            instances={instances}
            models={models}
            serverLogs={serverLogs}
            openaiURL={openaiURL}
            onRefresh={refresh}
            notify={notify}
          />
        )}
        {tab === 'runtimes' && (
          <RuntimesTab
            runtimes={runtimes}
            selections={selections}
            onRefresh={refresh}
            notify={notify}
          />
        )}
        {tab === 'sources' && (
          <SourcesTab
            sources={sources}
            builds={builds}
            logs={logs}
            backends={system?.hardware?.backends ?? ['cuda', 'vulkan', 'cpu']}
            config={config}
            onRefresh={refresh}
            notify={notify}
          />
        )}
        {tab === 'settings' && (
          <SettingsTab config={config} onRefresh={refresh} notify={notify} />
        )}
        </ErrorBoundary>
      </main>

      {toast && <div className={`toast ${toast.error ? 'error' : ''}`}>{toast.msg}</div>}
    </div>
  )
}

class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error) {
    console.error('RuntimeForge UI error:', error)
  }

  render() {
    if (this.state.error) {
      return (
        <div className="card">
          <h3>Something went wrong</h3>
          <div className="mono" style={{ color: 'var(--bad)' }}>
            {this.state.error.message}
          </div>
          <div className="muted" style={{ marginTop: 8 }}>
            The rest of the app is still usable. Switch tabs or reload.
          </div>
          <button style={{ marginTop: 12 }} onClick={() => this.setState({ error: null })}>
            Try again
          </button>
        </div>
      )
    }
    return this.props.children
  }
}

// --- Dashboard -------------------------------------------------------

function Dashboard(props: {
  system: SystemInfo | null
  instances: Instance[]
  builds: Job[]
  openaiURL: string
  restarting: boolean
  onUnload: (id: string) => void
  onRefreshEnv: () => void
  onRestart: () => void
}) {
  const hw = props.system?.hardware
  return (
    <>
      <h1>Dashboard</h1>
      <div className="sub">RuntimeForge builds and selects llama.cpp runtimes automatically.</div>

      <div className="cards">
        <div className="card">
          <h3>OpenAI-compatible API</h3>
          <div className="mono">{props.openaiURL}</div>
          <div className="muted" style={{ marginTop: 8, fontSize: 12 }}>
            Send <span className="mono">"model": "loaded"</span> to use whatever is loaded.
          </div>
        </div>

        <div className="card">
          <h3>Backends</h3>
          <div className="pill-list">
            {(hw?.backends ?? []).map((b) => (
              <span key={b} className="badge accent">
                {b}
              </span>
            ))}
          </div>
        </div>

        <div className="card">
          <h3>App root</h3>
          <div className="mono">{props.system?.root ?? '…'}</div>
        </div>
      </div>

      <h2>GPUs</h2>
      <div className="card">
        {(hw?.gpus ?? []).length === 0 && <div className="muted">No GPUs detected.</div>}
        {(hw?.gpus ?? []).map((g, i) => (
          <div className="gpu" key={i}>
            <div>
              <div>{g.name}</div>
              <div className="muted mono">
                {g.vendor}
                {g.driver ? ` · ${g.driver}` : ''}
                {g.memory_mb ? ` · ${(g.memory_mb / 1024).toFixed(1)} GiB` : ''}
              </div>
            </div>
            <div className="pill-list">
              {g.apis?.map((a) => (
                <span className="pill" key={a}>
                  {a}
                </span>
              ))}
            </div>
          </div>
        ))}
      </div>

      <h2>Toolchain</h2>
      <div className="card">
        {Object.values(hw?.tools ?? {}).map((t) => (
          <div className="tool" key={t.name}>
            <span>
              <span className={t.found ? 'badge good' : 'badge bad'}>{t.found ? 'ok' : 'missing'}</span>{' '}
              {t.name}
            </span>
            <span className="path mono">{t.version || t.path}</span>
          </div>
        ))}
      </div>
      <div className="row" style={{ marginTop: 10 }}>
        <button onClick={props.onRefreshEnv}>Re-detect environment</button>
        <button className="danger" onClick={props.onRestart} disabled={props.restarting}>
          {props.restarting ? 'Restarting…' : 'Restart service'}
        </button>
        {hw?.detected_at && (
          <span className="muted mono" style={{ fontSize: 11 }}>
            detected {new Date(hw.detected_at).toLocaleString()}
          </span>
        )}
      </div>

      <h2>Loaded models</h2>
      {props.instances.length === 0 ? (
        <div className="muted">Nothing loaded. Load a model from the Models tab.</div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Model</th>
              <th>Runtime</th>
              <th>Backend</th>
              <th>Status</th>
              <th>Port</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {props.instances.map((i) => (
              <tr key={i.id}>
                <td>{i.model_name}</td>
                <td className="mono">{i.runtime_id}</td>
                <td>{i.backend}</td>
                <td>
                  <StatusBadge status={i.status} error={i.error} />
                </td>
                <td className="mono">{i.port}</td>
                <td>
                  <button onClick={() => props.onUnload(i.model_id)}>Unload</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

// --- Models ----------------------------------------------------------

function ModelsTab(props: {
  models: Model[]
  instances: Instance[]
  runtimes: Manifest[]
  selections: Selections
  onRefresh: () => void
  notify: (msg: string, error?: boolean) => void
}) {
  const [sources, setSources] = useState<ModelSources>({
    scan_dirs: [],
    scan_depth: -1,
    roots: [],
    files: [],
  })
  const [rootPath, setRootPath] = useState('')
  const [rootDepth, setRootDepth] = useState('-1')
  const [filePath, setFilePath] = useState('')
  const [showProjectors, setShowProjectors] = useState(false)
  const [loadFor, setLoadFor] = useState<Model | null>(null)

  useEffect(() => {
    api
      .modelSources()
      .then(setSources)
      .catch(() => undefined)
  }, [])

  const byModel = useMemo(() => {
    const m: Record<string, Instance> = {}
    for (const i of props.instances) m[i.model_id] = i
    return m
  }, [props.instances])

  const roots = sources.roots ?? []
  const files = sources.files ?? []
  const allModels = props.models ?? []
  const projectorCount = allModels.filter((m) => m.projector).length
  const visible = showProjectors ? allModels : allModels.filter((m) => !m.projector)

  const persist = async (nextRoots: ModelRoot[], nextFiles: string[]) => {
    try {
      const res = await api.setModelSources({
        scan_dirs: sources.scan_dirs ?? [],
        scan_depth: sources.scan_depth,
        roots: nextRoots,
        files: nextFiles,
      })
      setSources(res.sources)
      const warn = res.errors?.length ? ` (${res.errors.length} warnings)` : ''
      props.notify(`Registered ${res.count} models${warn}`)
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  const scan = async () => {
    try {
      const res = await api.scanModels()
      const warn = res.errors?.length ? ` (${res.errors.length} warnings)` : ''
      props.notify(`Scanned ${res.count} models${warn}`)
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  const addRoot = () => {
    if (!rootPath.trim()) return
    const parsed = Number(rootDepth)
    const depth = Number.isFinite(parsed) ? parsed : -1
    void persist([...roots, { path: rootPath.trim(), depth }], files)
    setRootPath('')
  }

  const addFile = () => {
    if (!filePath.trim()) return
    void persist(roots, [...files, filePath.trim()])
    setFilePath('')
  }

  return (
    <>
      <h1>Models</h1>
      <div className="sub">Register folders or individual GGUF files, then load explicitly.</div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h3>Register models</h3>
        <div className="row">
          <input
            style={{ minWidth: 320 }}
            placeholder="folder path (e.g. ~/models)"
            value={rootPath}
            onChange={(e) => setRootPath(e.target.value)}
          />
          <input
            style={{ width: 140 }}
            placeholder="depth (-1 = all)"
            value={rootDepth}
            onChange={(e) => setRootDepth(e.target.value)}
          />
          <button onClick={addRoot} disabled={!rootPath.trim()}>
            Add folder
          </button>
        </div>
        <div className="row" style={{ marginTop: 10 }}>
          <input
            style={{ minWidth: 320 }}
            placeholder="/path/to/model.gguf"
            value={filePath}
            onChange={(e) => setFilePath(e.target.value)}
          />
          <button onClick={addFile} disabled={!filePath.trim()}>
            Add file
          </button>
          <span className="spacer" />
          <button className="primary" onClick={scan}>
            Rescan
          </button>
        </div>

        {(roots.length > 0 || files.length > 0) && (
          <div style={{ marginTop: 12 }}>
            {roots.map((r, i) => (
              <div className="tool" key={`root-${i}`}>
                <span>
                  <span className="badge">dir</span> {r.path}{' '}
                  <span className="muted">
                    (depth {r.depth < 0 ? 'all' : r.depth})
                  </span>
                </span>
                <button
                  className="danger"
                  onClick={() => persist(roots.filter((_, j) => j !== i), files)}
                >
                  Remove
                </button>
              </div>
            ))}
            {files.map((f, i) => (
              <div className="tool" key={`file-${i}`}>
                <span>
                  <span className="badge accent">file</span> {f}
                </span>
                <button
                  className="danger"
                  onClick={() => persist(roots, files.filter((_, j) => j !== i))}
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
        )}

        {(sources.scan_dirs?.length ?? 0) > 0 && (
          <div className="muted" style={{ marginTop: 8, fontSize: 12 }}>
            default dirs: {(sources.scan_dirs ?? []).join(', ')} (depth {sources.scan_depth})
          </div>
        )}
      </div>

      <div className="row" style={{ marginBottom: 10 }}>
        <span className="muted">
          {visible.length} model{visible.length === 1 ? '' : 's'}
          {projectorCount > 0 && !showProjectors ? ` · ${projectorCount} projectors hidden` : ''}
        </span>
        <span className="spacer" />
        <label className="muted" style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
          <input
            type="checkbox"
            checked={showProjectors}
            onChange={(e) => setShowProjectors(e.target.checked)}
          />
          show multimodal projectors
        </label>
      </div>

      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>Architecture</th>
            <th>Quant</th>
            <th>Size</th>
            <th>Runtime</th>
            <th>Status</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {visible.map((m) => {
            const inst = byModel[m.id]
            return (
              <Fragment key={m.id}>
                <tr>
                  <td title={m.meta_name && m.meta_name !== m.name ? `metadata name: ${m.meta_name}` : undefined}>
                    {m.name}
                    {m.projector && <span className="badge" style={{ marginLeft: 8 }}>projector</span>}
                    {m.shard_total > 1 && <span className="badge" style={{ marginLeft: 8 }}>{m.shard_total} shards</span>}
                  </td>
                  <td className="mono">{m.architecture || '?'}</td>
                  <td>{m.quantization || '?'}</td>
                  <td className="muted">{formatBytes(m.size_bytes)}</td>
                  <td>
                    <select
                      value={props.selections?.models?.[m.id] ?? ''}
                      disabled={!!inst}
                      title={inst ? 'unload to change the runtime' : 'pin this model to a runtime'}
                      onChange={async (e) => {
                        const runtimeID = e.target.value
                        try {
                          await api.setSelection({ model: m.id, runtime_id: runtimeID })
                          props.notify(
                            runtimeID ? `Pinned ${m.name} to ${runtimeID}` : `${m.name} set to auto`,
                          )
                          props.onRefresh()
                        } catch (err) {
                          props.notify(String(err), true)
                        }
                      }}
                    >
                      <option value="">auto</option>
                      {props.runtimes.map((r) => (
                        <option key={r.id} value={r.id}>
                          {r.id}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>{inst ? <StatusBadge status={inst.status} error={inst.error} /> : <span className="muted">idle</span>}</td>
                  <td>
                    {inst ? (
                      <button
                        className="danger"
                        onClick={async () => {
                          await api.unloadModel(m.id)
                          props.notify(`Unloaded ${m.name}`)
                          props.onRefresh()
                        }}
                      >
                        Unload
                      </button>
                    ) : (
                      <button disabled={m.projector} onClick={() => setLoadFor(m)}>
                        Config
                      </button>
                    )}
                  </td>
                </tr>
              </Fragment>
            )
          })}
        </tbody>
      </table>

      {loadFor && (
        <Modal title={`Config: ${loadFor.name}`} onClose={() => setLoadFor(null)}>
          <ModelSettingsForm
            model={loadFor}
            onClose={() => setLoadFor(null)}
            notify={props.notify}
          />
        </Modal>
      )}
    </>
  )
}

// --- Modal + model settings form ------------------------------------

function Modal(props: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') props.onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [props.onClose])
  return (
    <div className="modal-backdrop" onClick={props.onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <h3>{props.title}</h3>
          <button onClick={props.onClose}>Close</button>
        </div>
        {props.children}
      </div>
    </div>
  )
}

// --- Model argument editor (name / value rows) ----------------------

type ArgDraft = { id: number; name: string; value: string }

function ModelSettingsForm(props: {
  model: Model
  onClose: () => void
  notify: (msg: string, error?: boolean) => void
}) {
  const [saved, setSaved] = useState(false)
  const [defaults, setDefaults] = useState<Arg[]>([])
  const nextID = useRef(1)
  const blank = (): ArgDraft => ({ id: nextID.current++, name: '', value: '' })
  const [rows, setRows] = useState<ArgDraft[]>(() => [blank()])

  const splitDefaults = (eff: Arg[], savedCount: number) =>
    eff.slice(0, Math.max(0, eff.length - savedCount))

  useEffect(() => {
    api
      .modelSettings(props.model.id)
      .then((r) => {
        const savedArgs = r.saved?.args ?? []
        setSaved(r.has_saved)
        setDefaults(splitDefaults(r.effective?.args ?? [], savedArgs.length))
        setRows(
          savedArgs.length
            ? savedArgs.map((a) => ({ id: nextID.current++, name: a.name, value: a.value }))
            : [blank()],
        )
      })
      .catch(() => undefined)
  }, [props.model.id])

  const update = (i: number, patch: Partial<ArgDraft>) =>
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  const add = () => setRows((rs) => [...rs, blank()])
  const remove = (i: number) =>
    setRows((rs) => {
      const next = rs.filter((_, j) => j !== i)
      return next.length ? next : [blank()]
    })

  const collect = () =>
    rows.map((r) => ({ name: r.name.trim(), value: r.value })).filter((a) => a.name !== '')

  const save = async () => {
    try {
      const args = collect()
      const res = await api.saveModelSettings(props.model.id, { args })
      setSaved(true)
      setDefaults(splitDefaults(res.effective?.args ?? [], args.length))
      props.notify('Saved model arguments')
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  const clear = async () => {
    try {
      await api.clearModelSettings(props.model.id)
      const r = await api.modelSettings(props.model.id)
      setSaved(false)
      setDefaults(r.effective?.args ?? [])
      setRows([blank()])
      props.notify('Cleared saved arguments')
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  return (
    <div>
      <div className="muted" style={{ fontSize: 12, marginBottom: 10 }}>
        llama.cpp arguments passed to <span className="mono">llama-server</span> after the model path. A value may
        be empty for a standalone flag, and may hold several space-separated (or &quot;quoted&quot;) tokens. Applied
        when the model is loaded from the <strong>Server</strong> tab.
      </div>

      <div className="arg-rows">
        <div className="arg-row arg-head">
          <span>Argument</span>
          <span>Value</span>
          <span />
        </div>
        {rows.map((r, i) => (
          <div className="arg-row" key={r.id}>
            <input
              className="mono"
              value={r.name}
              onChange={(e) => update(i, { name: e.target.value })}
              placeholder="--no-webui"
              spellCheck={false}
            />
            <input
              className="mono"
              value={r.value}
              onChange={(e) => update(i, { value: e.target.value })}
              placeholder="8192"
              spellCheck={false}
            />
            <button className="arg-del" title="Remove argument" onClick={() => remove(i)}>
              ×
            </button>
          </div>
        ))}
      </div>

      <div className="row" style={{ marginTop: 8 }}>
        <button onClick={add}>+ Add argument</button>
      </div>

      {defaults.length > 0 && (
        <div className="muted mono" style={{ fontSize: 11, marginTop: 10 }}>
          global defaults: {defaults.map((a) => [a.name, a.value].filter(Boolean).join(' ')).join(' ')}
        </div>
      )}

      <div className="row" style={{ marginTop: 14 }}>
        <button className="primary" onClick={save}>
          Save
        </button>
        {saved && (
          <>
            <span className="badge accent">saved</span>
            <button className="danger" onClick={clear}>
              Clear
            </button>
          </>
        )}
        <span className="spacer" />
        <button onClick={props.onClose}>Cancel</button>
      </div>
    </div>
  )
}

// --- Server / load ---------------------------------------------------

function ServerTab(props: {
  instances: Instance[]
  models: Model[]
  serverLogs: Record<string, string[]>
  openaiURL: string
  onRefresh: () => void
  notify: (msg: string, error?: boolean) => void
}) {
  const [loadModelID, setLoadModelID] = useState('')
  const [selected, setSelected] = useState('')

  const load = async () => {
    if (!loadModelID) return
    try {
      await api.loadModel(loadModelID, {})
      props.notify('Loading…')
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  const chatModels = (props.models ?? []).filter((m) => !m.projector)
  const selectedInst = props.instances.find((i) => i.id === selected) ?? props.instances[0]
  const logLines = selectedInst ? props.serverLogs[selectedInst.id] ?? selectedInst.tail ?? [] : []

  return (
    <>
      <h1>Server</h1>
      <div className="sub">
        Load a model and watch its llama-server logs. The OpenAI-compatible endpoint is{' '}
        <span className="mono">{props.openaiURL}</span>. Per-model settings are edited from the{' '}
        <strong>Models</strong> tab (<span className="mono">Config</span>).
      </div>

      <h2>Load a model</h2>
      <div className="card">
        <div className="row">
          <select value={loadModelID} onChange={(e) => setLoadModelID(e.target.value)} style={{ minWidth: 320 }}>
            <option value="">select a model…</option>
            {chatModels.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name} ({m.architecture})
              </option>
            ))}
          </select>
          <button className="primary" onClick={load} disabled={!loadModelID}>
            Load
          </button>
        </div>
      </div>

      <h2>Loaded models</h2>
      {props.instances.length === 0 ? (
        <div className="muted">Nothing loaded yet.</div>
      ) : (
        <table>
          <thead>
            <tr>
              <th>Model</th>
              <th>Runtime</th>
              <th>Backend</th>
              <th>Status</th>
              <th>Port</th>
              <th>Logs</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {props.instances.map((i) => (
              <tr key={i.id}>
                <td>{i.model_name}</td>
                <td className="mono">{i.runtime_id}</td>
                <td>{i.backend}</td>
                <td>
                  <StatusBadge status={i.status} error={i.error} />
                </td>
                <td className="mono">{i.port}</td>
                <td>
                  <button onClick={() => setSelected(i.id)}>Show</button>
                </td>
                <td>
                  <button
                    className="danger"
                    onClick={async () => {
                      try {
                        await api.unloadModel(i.model_id)
                        props.notify(`Unloaded ${i.model_name}`)
                        props.onRefresh()
                      } catch (e) {
                        props.notify(String(e), true)
                      }
                    }}
                  >
                    Unload
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <h2>Inference log</h2>
      <div className="logbox" style={{ maxHeight: 420 }}>
        {selectedInst
          ? logLines.slice(-400).join('\n') || 'no output yet'
          : 'Load a model to see its llama-server log.'}
      </div>
    </>
  )
}

// --- Runtimes --------------------------------------------------------

function RuntimesTab(props: {
  runtimes: Manifest[]
  selections: Selections
  onRefresh: () => void
  notify: (msg: string, error?: boolean) => void
}) {
  const setDefault = async (runtimeID: string) => {
    try {
      await api.setSelection({ format: 'gguf', runtime_id: runtimeID })
      props.notify('Default GGUF runtime updated')
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  return (
    <>
      <h1>Runtimes</h1>
      <div className="sub">Built llama.cpp variants. Each is a self-contained install.</div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h3>Default runtime for GGUF</h3>
        <div className="row">
          <select
            value={props.selections?.formats?.['gguf'] ?? ''}
            onChange={(e) => setDefault(e.target.value)}
          >
            <option value="">auto (architecture + backend)</option>
            {props.runtimes.map((r) => (
              <option key={r.id} value={r.id}>
                {r.id}
              </option>
            ))}
          </select>
          <span className="muted">Applies to models without a per-model override.</span>
        </div>
      </div>

      {props.runtimes.length === 0 && <div className="muted">No runtimes built yet. Use Sources → Build.</div>}
      {props.runtimes.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Runtime</th>
              <th>Backend</th>
              <th>Architectures</th>
              <th>Built</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {props.runtimes.map((r) => (
              <tr key={r.id}>
                <td className="mono">{r.id}</td>
                <td>
                  <span className="badge accent">{r.backend}</span>
                </td>
                <td className="muted">{r.supported_architectures?.length ?? 0}</td>
                <td className="muted">{new Date(r.built_at).toLocaleString()}</td>
                <td>
                  <button
                    className="danger"
                    onClick={async () => {
                      if (!confirm(`Delete ${r.id}?`)) return
                      await api.removeRuntime(r.id)
                      props.notify(`Removed ${r.id}`)
                      props.onRefresh()
                    }}
                  >
                    Delete
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

// --- Sources ---------------------------------------------------------

function SourcesTab(props: {
  sources: Source[]
  builds: Job[]
  logs: Record<string, string[]>
  backends: string[]
  config: unknown
  onRefresh: () => void
  notify: (msg: string, error?: boolean) => void
}) {
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [ref, setRef] = useState('')
  const [openJob, setOpenJob] = useState<string | null>(null)
  const [buildFor, setBuildFor] = useState<string | null>(null)
  const [sel, setSel] = useState<Record<string, boolean>>({})
  const [advOpen, setAdvOpen] = useState(false)
  const [cmakeText, setCmakeText] = useState('')
  const [extraCfg, setExtraCfg] = useState('')
  const [extraBuild, setExtraBuild] = useState('')
  const [cc, setCc] = useState('')
  const [cxx, setCxx] = useState('')
  const [generator, setGenerator] = useState('')
  const [buildType, setBuildType] = useState('')
  const [jobs, setJobs] = useState('')

  const buildCfg = (props.config as { build?: Record<string, unknown> } | null)?.build ?? {}

  const openBuild = (sourceName: string) => {
    const next = buildFor === sourceName ? null : sourceName
    setBuildFor(next)
    if (next) {
      setSel(Object.fromEntries(props.backends.map((b) => [b, true])))
      // Prefill from the saved configuration so nothing is hidden.
      setCc(String(buildCfg.cc ?? ''))
      setCxx(String(buildCfg.cxx ?? ''))
      setGenerator(String(buildCfg.generator ?? 'Ninja'))
      setBuildType(String(buildCfg.build_type ?? 'Release'))
      setJobs(String(buildCfg.parallel_jobs ?? 0))
      setCmakeText('')
      setExtraCfg('')
      setExtraBuild('')
      setAdvOpen(false)
    }
  }

  const parseDefines = (text: string): Record<string, string> => {
    const out: Record<string, string> = {}
    for (const raw of text.split('\n')) {
      const line = raw.trim()
      if (!line || line.startsWith('#')) continue
      const eq = line.indexOf('=')
      if (eq < 0) continue
      out[line.slice(0, eq).trim()] = line.slice(eq + 1).trim()
    }
    return out
  }

  const startBuild = async (sourceName: string) => {
    const backends = props.backends.filter((b) => sel[b])
    const body = {
      backends,
      cmake_defines: parseDefines(cmakeText),
      extra_configure_args: extraCfg.split(/\s+/).filter(Boolean),
      extra_build_args: extraBuild.split(/\s+/).filter(Boolean),
      cc: cc || undefined,
      cxx: cxx || undefined,
      generator: generator || undefined,
      build_type: buildType || undefined,
      parallel_jobs: jobs ? Number(jobs) : undefined,
    }
    try {
      await api.build(sourceName, body)
      props.notify(`Build queued: ${backends.join(', ')}`)
      setBuildFor(null)
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  const add = async () => {
    try {
      await api.addSource({ name, url, ref })
      setName('')
      setUrl('')
      setRef('')
      props.notify('Source added')
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  return (
    <>
      <h1>Sources</h1>
      <div className="sub">
        Git repositories that provide llama.cpp runtimes. Check detects updates via ls-remote; Build is separate.
      </div>

      <div className="card" style={{ marginBottom: 16 }}>
        <h3>Add a source</h3>
        <div className="row">
          <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <input
            style={{ minWidth: 320 }}
            placeholder="https://github.com/you/llama.cpp"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
          />
          <input placeholder="ref (branch/tag/commit)" value={ref} onChange={(e) => setRef(e.target.value)} />
          <button className="primary" onClick={add} disabled={!url}>
            Add
          </button>
        </div>
      </div>

      <table>
        <thead>
          <tr>
            <th>Name</th>
            <th>URL</th>
            <th>Ref</th>
            <th>Status</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {props.sources.map((s) => (
            <Fragment key={s.name}>
              <tr>
                <td>{s.name}</td>
                <td className="mono muted" style={{ maxWidth: 280, overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {s.url}
                </td>
                <td className="mono">{s.ref || 'HEAD'}</td>
                <td>
                  {s.last_error ? (
                    <span className="badge bad">error</span>
                  ) : s.update_available ? (
                    <span className="badge warn">update</span>
                  ) : s.remote_commit ? (
                    <span className="badge good">up to date</span>
                  ) : (
                    <span className="muted">not checked</span>
                  )}
                </td>
                <td>
                  <div className="row">
                    <button
                      onClick={async () => {
                        try {
                          const r = await api.checkSource(s.name)
                          props.notify(r.update_available ? 'Update available' : 'Up to date')
                          props.onRefresh()
                        } catch (e) {
                          props.notify(String(e), true)
                        }
                      }}
                    >
                      Check
                    </button>
                    <button
                      className="primary"
                      onClick={() => openBuild(s.name)}
                    >
                      Build…
                    </button>
                    <button
                      className="danger"
                      onClick={async () => {
                        if (!confirm(`Remove source ${s.name}?`)) return
                        await api.removeSource(s.name)
                        props.onRefresh()
                      }}
                    >
                      Remove
                    </button>
                  </div>
                </td>
              </tr>
              {buildFor === s.name && (
                <tr>
                  <td colSpan={5} className="expand">
                    <div className="row">
                      <span className="muted">Backends to build:</span>
                      {props.backends.map((b) => (
                        <label
                          key={b}
                          className="muted"
                          style={{ display: 'flex', alignItems: 'center', gap: 6 }}
                        >
                          <input
                            type="checkbox"
                            checked={!!sel[b]}
                            onChange={(e) => setSel({ ...sel, [b]: e.target.checked })}
                          />
                          {b}
                        </label>
                      ))}
                      <span className="spacer" />
                      <button
                        className="primary"
                        disabled={!props.backends.some((b) => sel[b])}
                        onClick={() => startBuild(s.name)}
                      >
                        Start build
                      </button>
                      <button onClick={() => setBuildFor(null)}>Cancel</button>
                    </div>

                    <div className="row" style={{ marginTop: 8 }}>
                      <button onClick={() => setAdvOpen(!advOpen)}>
                        {advOpen ? 'Hide' : 'Advanced'} build arguments
                      </button>
                      <span className="muted" style={{ fontSize: 12 }}>
                        Overrides apply to this build only; blank keeps the saved settings.
                      </span>
                    </div>

                    {advOpen && (
                      <div className="form-grid" style={{ marginTop: 8 }}>
                        <div>
                          <label>CC</label>
                          <input value={cc} onChange={(e) => setCc(e.target.value)} placeholder="gcc-15" />
                        </div>
                        <div>
                          <label>CXX</label>
                          <input value={cxx} onChange={(e) => setCxx(e.target.value)} placeholder="g++-15" />
                        </div>
                        <div>
                          <label>Generator</label>
                          <input value={generator} onChange={(e) => setGenerator(e.target.value)} />
                        </div>
                        <div>
                          <label>Build type</label>
                          <input value={buildType} onChange={(e) => setBuildType(e.target.value)} />
                        </div>
                        <div>
                          <label>Parallel jobs (0=auto)</label>
                          <input value={jobs} onChange={(e) => setJobs(e.target.value)} />
                        </div>
                        <div style={{ gridColumn: '1 / -1' }}>
                          <label>CMake defines (one KEY=VALUE per line)</label>
                          <textarea
                            className="mono"
                            style={{ width: '100%', minHeight: 70 }}
                            value={cmakeText}
                            onChange={(e) => setCmakeText(e.target.value)}
                            placeholder={'CMAKE_CUDA_ARCHITECTURES=120\nGGML_CUDA_FA_ALL_QUANTS=ON'}
                            spellCheck={false}
                          />
                        </div>
                        <div style={{ gridColumn: '1 / -1' }}>
                          <label>Extra configure args</label>
                          <input
                            style={{ width: '100%' }}
                            value={extraCfg}
                            onChange={(e) => setExtraCfg(e.target.value)}
                            placeholder="-DLLAMA_CURL=OFF"
                          />
                        </div>
                        <div style={{ gridColumn: '1 / -1' }}>
                          <label>Extra build args</label>
                          <input
                            style={{ width: '100%' }}
                            value={extraBuild}
                            onChange={(e) => setExtraBuild(e.target.value)}
                            placeholder="--verbose"
                          />
                        </div>
                      </div>
                    )}
                    <div className="muted" style={{ marginTop: 8, fontSize: 12 }}>
                      Selected backends are compiled <strong>together in one CMake build</strong> into a single
                      runtime (llama.cpp picks the device at runtime). Cancel stops it immediately.
                    </div>
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>

      <h2>Builds</h2>
      {props.builds.length === 0 && <div className="muted">No builds yet.</div>}
      {props.builds.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Source</th>
              <th>Backend</th>
              <th>Status</th>
              <th>Started</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {props.builds.map((j) => (
              <Fragment key={j.id}>
                <tr>
                  <td>{j.source}</td>
                  <td>
                    <span className="badge accent">{j.backend}</span>
                  </td>
                  <td>
                    <StatusBadge status={j.status} error={j.error} />
                  </td>
                  <td className="muted">{j.started_at ? new Date(j.started_at).toLocaleTimeString() : '—'}</td>
                  <td>
                    <div className="row">
                      <button onClick={() => setOpenJob(openJob === j.id ? null : j.id)}>
                        {openJob === j.id ? 'Hide log' : 'Log'}
                      </button>
                      {j.status === 'running' && (
                        <button className="danger" onClick={() => api.cancelBuild(j.id)}>
                          Cancel
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
                {openJob === j.id && (
                  <tr>
                    <td colSpan={5}>
                      <div className="logbox">
                        {(props.logs[j.id] ?? j.tail ?? []).slice(-200).join('\n') || 'no output yet'}
                      </div>
                    </td>
                  </tr>
                )}
              </Fragment>
            ))}
          </tbody>
        </table>
      )}
    </>
  )
}

// --- Settings --------------------------------------------------------

function SettingsTab(props: {
  config: unknown
  onRefresh: () => void
  notify: (msg: string, error?: boolean) => void
}) {
  const [text, setText] = useState('')
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    if (!dirty) setText(JSON.stringify(props.config, null, 2))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.config])

  const save = async () => {
    try {
      const parsed = JSON.parse(text)
      // Persist only what changed so the overlay stays minimal and does
      // not freeze unrelated resolved values like the bind port.
      const patch = deepDiff(props.config, parsed)
      if (patch === undefined) {
        props.notify('No changes to save')
        setDirty(false)
        return
      }
      await api.updateConfig(patch)
      props.notify('Configuration saved')
      setDirty(false)
      props.onRefresh()
    } catch (e) {
      props.notify(String(e), true)
    }
  }

  return (
    <>
      <h1>Settings</h1>
      <div className="sub">
        Effective configuration. Saving merges your changes into{' '}
        <span className="mono">config.d/90-user.toml</span> — every key is overridable and comments in{' '}
        <span className="mono">config.toml</span> are preserved.
      </div>
      <textarea
        style={{ width: '100%', minHeight: 460 }}
        className="mono"
        value={text}
        onChange={(e) => {
          setText(e.target.value)
          setDirty(true)
        }}
        spellCheck={false}
      />
      <div className="row" style={{ marginTop: 10 }}>
        <button className="primary" onClick={save} disabled={!dirty}>
          Save
        </button>
        <button
          onClick={() => {
            setText(JSON.stringify(props.config, null, 2))
            setDirty(false)
          }}
          disabled={!dirty}
        >
          Revert
        </button>
        {dirty && <span className="muted">unsaved changes</span>}
      </div>
    </>
  )
}

function isPlainObject(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
}

// deepDiff returns the minimal patch that turns base into next, or
// undefined when they are equal.
function deepDiff(base: unknown, next: unknown): Record<string, unknown> | undefined {
  if (isPlainObject(base) && isPlainObject(next)) {
    const out: Record<string, unknown> = {}
    for (const key of Object.keys(next)) {
      const d = deepDiff(base[key], next[key])
      if (d !== undefined) out[key] = d
    }
    return Object.keys(out).length > 0 ? out : undefined
  }
  if (JSON.stringify(base) === JSON.stringify(next)) return undefined
  return next as Record<string, unknown>
}

function StatusBadge(props: { status: string; error?: string }) {
  const cls =
    props.status === 'ready' || props.status === 'succeeded'
      ? 'good'
      : props.status === 'error' || props.status === 'failed'
        ? 'bad'
        : props.status === 'canceled'
          ? 'warn'
          : 'accent'
  return (
    <span className={`badge ${cls}`} title={props.error}>
      {props.status}
    </span>
  )
}

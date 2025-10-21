import { lazy, createResource, createSignal, For, Show, createEffect, onCleanup, createMemo } from 'solid-js'
import {
  A,
  Route,
  Router,
  useNavigate,
  useParams,
  type RouteSectionProps
} from '@solidjs/router'
import Toaster, { pushToast } from './components/Toaster'
import { listClusters, createClusterRecord, attachClusterKubeconfig, getHealthSummary, clusterHealth } from './lib/api'
import Modal from './components/Modal'
import { apiUrl } from './lib/config'

const Servers = lazy(() => import('./routes/Servers'))
const ServerDetail = lazy(() => import('./routes/ServerDetail'))
const Launch = lazy(() => import('./routes/Launch'))
const Databases = lazy(() => import('./routes/Databases'))
const Settings = lazy(() => import('./routes/Settings'))
// Add missing database detail + table routes
const DatabaseDetail = lazy(() => import('./routes/DatabaseDetail'))
const TableView = lazy(() => import('./routes/TableView'))
const TableSchema = lazy(() => import('./routes/TableSchema'))
const TableAudit = lazy(() => import('./routes/TableAudit'))
const TablePermissions = lazy(() => import('./routes/TablePermissions'))
const TableImportExport = lazy(() => import('./routes/TableImportExport'))

const Home = () => (
  <div class="p-6 text-sm text-neutral-600">
    <div class="font-semibold mb-2">No cluster selected</div>
    <div>Select a cluster from the sidebar or click Add to connect one.</div>
  </div>
)

function Sidebar() {
  const navigate = useNavigate()
  const [clusters, { refetch }] = createResource(listClusters)
  const [health, { refetch: refetchHealth }] = createResource(getHealthSummary)
  const [busy, setBusy] = createSignal(false)
  const [kc, setKc] = createSignal('')
  const [open, setOpen] = createSignal(false)
  const [name, setName] = createSignal('')
  const [healthTs, setHealthTs] = createSignal<number | null>(null)

  const importJoinFile = async (file: File) => {
    try {
      const txt = await file.text()
      const obj = JSON.parse(txt)
      if (obj?.cluster?.name) setName(String(obj.cluster.name))
      if (obj?.cluster?.kubeconfig) setKc(String(obj.cluster.kubeconfig))
      if (obj?.ui?.vite_api_base) {
        const base = String(obj.ui.vite_api_base)
        if (base && typeof window !== 'undefined') {
          const current = sessionStorage.getItem('GN_VITE_API_BASE') || ''
          if (current !== base) {
            const ok = confirm('Use API base from join file and reload now?')
            if (ok) {
              sessionStorage.setItem('GN_VITE_API_BASE', base)
              // Reload to pick up new base for all requests
              location.reload()
              return
            } else {
              // Store it for later, but do not reload
              sessionStorage.setItem('GN_VITE_API_BASE', base)
            }
          }
        }
      }
      pushToast({ type: 'success', message: 'Join file imported' })
    } catch (e) {
      pushToast({ type: 'error', message: 'Invalid join file' })
    }
  }

  const looksLikeKubeconfig = (s: string) => {
    const t = s.trim()
    if (!t) return false
    // Very light heuristic to avoid obvious paste mistakes
    return /apiVersion:\s*v1/i.test(t) && /(clusters|contexts|users):/i.test(t)
  }

  const canSave = createMemo(() => {
    const pasted = kc().trim()
    if (!pasted) return true
    return looksLikeKubeconfig(pasted)
  })

  const startWizard = () => {
    setOpen(true)
  }

  const submitWizard = async () => {
    if (busy()) return
    const pasted = kc().trim()
    if (pasted && !looksLikeKubeconfig(pasted)) {
      pushToast({ type: 'error', message: 'Kubeconfig does not look valid' })
      return
    }
    setBusy(true)
    try {
      const rec = await createClusterRecord(name().trim() || undefined)
      if (!rec?.id) {
        pushToast({ type: 'error', message: 'Failed to create cluster record' })
        return
      }
      if (pasted) {
        const ok = await attachClusterKubeconfig(rec.id, pasted)
        if (!ok) {
          pushToast({ type: 'error', message: 'Attach failed. Fix the kubeconfig and try again.' })
          return
        }
        // Check health immediately and inform the user
        try {
          const st = await clusterHealth(rec.id)
          if (st !== 'ok') {
            pushToast({ type: 'info', message: `Cluster not reachable yet (${st}). You can attach a different kubeconfig in Settings.` })
          } else {
            pushToast({ type: 'success', message: 'Cluster connected' })
          }
        } catch {}
      }
      setKc('')
      setName('')
      setOpen(false)
      refetch()
      navigate(`/c/${encodeURIComponent(rec.id)}/servers`)
    } catch (e) {
      pushToast({ type: 'error', message: (e as Error).message || 'Error' })
    } finally {
      setBusy(false)
    }
  }

  const ClusterRow = (props: { id: string; name?: string }) => {
    const status = () => {
      const h = health()
      const m = new Map((h?.clusters || []).map((c: any) => [c.id, c.status]))
      return (m.get(props.id) as string) || 'unknown'
    }
    const tooltip = () => {
      const h = health()
      const m = new Map((h?.clusters || []).map((c: any) => [c.id, c]))
      const item = m.get(props.id) as any
      if (!item) return `Health: ${status()}`
      if (item.status === 'error') {
        const parts = ["Health: error"]
        if (item.code) parts.push(`code=${item.code}`)
        if (item.error) parts.push(item.error)
        return parts.join(' — ')
      }
      if (item.status === 'unknown' && item.code) {
        return `Health: unknown — code=${item.code}`
      }
      return `Health: ${item.status}`
    }
    const dot = () => {
      const h = status()
      if (h === 'ok') return 'bg-green-500'
      if (h === 'error') return 'bg-red-500'
      return 'bg-neutral-400'
    }
    return (
      <A href={`/c/${encodeURIComponent(props.id)}/servers`} class="flex items-center gap-2 px-2 py-1 rounded hover:bg-neutral-100 dark:hover:bg-neutral-800" title={tooltip()}>
        <span class={`w-2 h-2 rounded-full ${dot()}`} />
        <span class="truncate text-sm">{props.name || props.id}</span>
        <span class="ml-auto text-[10px] text-neutral-500 uppercase">{status()}</span>
      </A>
    )
  }

  // refresh health summary periodically
  let htimer: number | undefined
  createEffect(() => {
    if (htimer) window.clearInterval(htimer)
    // store timestamp when health fetched
    refetchHealth()
    setHealthTs(Date.now())
    htimer = window.setInterval(() => { refetchHealth(); setHealthTs(Date.now()) }, 30000)
    onCleanup(() => { if (htimer) window.clearInterval(htimer) })
  })

  const lastUpdated = createMemo(() => {
    const t = healthTs()
    if (!t) return ''
    const d = Math.round((Date.now() - t) / 1000)
    return d <= 0 ? 'just now' : `${d}s ago`
  })

  return (
    <aside class="w-64 border-r bg-neutral-50/40 dark:bg-neutral-900/30 p-3 space-y-3">
      <div class="flex items-center justify-between">
        <div class="font-semibold text-sm">Clusters</div>
        <div class="flex items-center gap-2">
          <button class="btn" onClick={startWizard}>Add</button>
        </div>
      </div>
      <div class="space-y-1">
        <For each={clusters() ?? []}>
          {(c) => (
            <ClusterRow id={c.id} name={c.name} />
          )}
        </For>
      </div>
      <div class="text-[10px] text-neutral-500">Health: {lastUpdated()}</div>
      <Modal
        title="Connect a cluster"
        open={open()}
        onClose={() => { if (!busy()) setOpen(false) }}
        footer={
          <>
            <button class="btn" onClick={() => setOpen(false)} disabled={busy()}>Cancel</button>
            <button class="btn" onClick={submitWizard} disabled={busy() || !canSave()}>{busy() ? 'Connecting…' : 'Save'}</button>
          </>
        }
      >
        <div class="space-y-3">
          <label class="block text-sm">
            Name (optional)
            <input class="mt-1 w-full rounded-md border px-3 py-2" value={name()} onInput={e => setName(e.currentTarget.value)} />
          </label>
          <div class="flex items-center justify-between gap-2">
            <div class="text-sm font-medium">Join file</div>
            <label class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700 cursor-pointer">
              <input type="file" accept=".json,.config,application/json" class="hidden" onChange={(e) => {
                const f = e.currentTarget.files?.[0]
                if (f) importJoinFile(f)
              }} />
              Import…
            </label>
          </div>
          <label class="block text-sm">
            Paste kubeconfig (optional)
            <textarea class="mt-1 w-full h-40 rounded-md border px-3 py-2 font-mono text-xs" placeholder="Paste kubeconfig YAML" value={kc()} onInput={e => setKc(e.currentTarget.value)} />
            <div class="text-xs text-neutral-500 mt-1">If omitted, you can attach later in Settings.</div>
          </label>
        </div>
      </Modal>
    </aside>
  )
}

function ClusterShell(props: RouteSectionProps) {
  const params = useParams()
  const cid = () => params.clusterId || ''
  const enc = (s: string) => encodeURIComponent(s || '')

  // Fetch per-host cluster status to gate UI features when cluster not configured
  const fetchStatus = async (id: string) => {
    if (!id) return null
    try {
      const res = await fetch(apiUrl(`/api/cluster/${encodeURIComponent(id)}/status`), { cache: 'no-store' })
      if (!res.ok) return null
      return await res.json()
    } catch (e) {
      return null
    }
  }
  const [status] = createResource(() => cid(), fetchStatus)

  return (
    <div class="min-h-screen flex flex-col">
      <header class="border-b sticky top-0 z-10 bg-white/70 dark:bg-neutral-900/70 backdrop-blur">
        <div class="px-4 sm:px-6 lg:px-8 flex items-center gap-4 h-12">
          <A href="/" class="font-semibold">GuildNet</A>
          <Show when={!!cid()}>
            <nav class="flex items-center gap-3 text-sm">
              {/* Only show Servers/Launch/Databases when cluster status indicates kube is reachable or kubeconfig present+valid. Otherwise show only Settings. */}
              <Show when={status() && (status().k8sReachable === true || (status().kubeconfigPresent && status().kubeconfigValid))} fallback={<A href={`/c/${enc(cid())}/settings`} activeClass="text-brand-600" class="hover:underline">Settings</A>}>
                <>
                  <A href={`/c/${enc(cid())}/servers`} activeClass="text-brand-600" class="hover:underline">Servers</A>
                  <A href={`/c/${enc(cid())}/launch`} activeClass="text-brand-600" class="hover:underline">Launch</A>
                  <A href={`/c/${enc(cid())}/databases`} activeClass="text-brand-600" class="hover:underline">Databases</A>
                  <A href={`/c/${enc(cid())}/settings`} activeClass="text-brand-600" class="hover:underline">Settings</A>
                </>
              </Show>
            </nav>
          </Show>
        </div>
      </header>
      <div class="flex flex-1 min-h-0">
        <Sidebar />
        <main class="flex-1 px-4 sm:px-6 lg:px-8 py-4 overflow-auto">
          {props.children}
        </main>
      </div>
      <Toaster />
    </div>
  )
}

export default function App() {
  return (
    <Router>
      <Route path="/" component={ClusterShell}>
        <Route path="/c/:clusterId" component={Servers} />
        <Route path="/c/:clusterId/servers" component={Servers} />
        <Route path="/c/:clusterId/servers/:id" component={ServerDetail} />
        <Route path="/c/:clusterId/launch" component={Launch} />
        <Route path="/c/:clusterId/databases" component={Databases} />
        {/* Database details and table routes */}
        <Route path="/c/:clusterId/databases/:dbId" component={DatabaseDetail} />
        <Route path="/c/:clusterId/databases/:dbId/tables/:table" component={TableView} />
        <Route path="/c/:clusterId/databases/:dbId/tables/:table/schema" component={TableSchema} />
        <Route path="/c/:clusterId/databases/:dbId/tables/:table/audit" component={TableAudit} />
        <Route path="/c/:clusterId/databases/:dbId/tables/:table/permissions" component={TablePermissions} />
        <Route path="/c/:clusterId/databases/:dbId/tables/:table/import-export" component={TableImportExport} />
        <Route path="/c/:clusterId/settings" component={Settings} />
        {/* Home when no cluster */}
        <Route path="/" component={Home} />
      </Route>
    </Router>
  )
}

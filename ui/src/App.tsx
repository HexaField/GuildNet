import { lazy, createResource, For, Show, createEffect, onCleanup, createMemo, createSignal } from 'solid-js'
import {
  A,
  Route,
  Router,
  useNavigate,
  useParams,
  type RouteSectionProps
} from '@solidjs/router'
import Toaster from './components/Toaster'
import { listClusters, getHealthSummary } from './lib/api'
import { apiUrl } from './lib/config'

const Servers = lazy(() => import('./routes/Servers'))
const ServerDetail = lazy(() => import('./routes/ServerDetail'))
const Launch = lazy(() => import('./routes/Launch'))
const Databases = lazy(() => import('./routes/Databases'))
const Settings = lazy(() => import('./routes/Settings'))
const Deploy = lazy(() => import('./routes/Deploy'))
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
  const [healthTs, setHealthTs] = createSignal<number | null>(null)

  const startWizard = () => navigate('/deploy')


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
        const parts = ['Health: error']
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
      <A
        href={`/c/${encodeURIComponent(props.id)}/servers`}
        class="flex items-center gap-2 px-2 py-1 rounded hover:bg-neutral-100 dark:hover:bg-neutral-800"
        title={tooltip()}
      >
        <span class={`w-2 h-2 rounded-full ${dot()}`} />
        <span class="truncate text-sm">{props.name || props.id}</span>
        <span class="ml-auto text-[10px] text-neutral-500 uppercase">
          {status()}
        </span>
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
    htimer = window.setInterval(() => {
      refetchHealth()
      setHealthTs(Date.now())
    }, 30000)
    onCleanup(() => {
      if (htimer) window.clearInterval(htimer)
    })
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
          <button class="btn" onClick={startWizard}>
            Add
          </button>
        </div>
      </div>
      <div class="space-y-1">
        <For each={clusters() ?? []}>
          {(c) => <ClusterRow id={c.id} name={c.name} />}
        </For>
      </div>
      <div class="text-[10px] text-neutral-500">Health: {lastUpdated()}</div>
      {/* Modal removed; use the Deployment Manager page instead */}
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
      const res = await fetch(
        apiUrl(`/api/cluster/${encodeURIComponent(id)}/status`),
        { cache: 'no-store' }
      )
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
          <A href="/" class="font-semibold">
            GuildNet
          </A>
          <Show when={!!cid()}>
            <nav class="flex items-center gap-3 text-sm">
              {/* Only show Servers/Launch/Databases when cluster status indicates kube is reachable or kubeconfig present+valid. Otherwise show only Settings. */}
              <Show
                when={
                  status() &&
                  (status().k8sReachable === true ||
                    (status().kubeconfigPresent && status().kubeconfigValid))
                }
                fallback={
                  <A
                    href={`/c/${enc(cid())}/settings`}
                    activeClass="text-brand-600"
                    class="hover:underline"
                  >
                    Settings
                  </A>
                }
              >
                <>
                  <A
                    href={`/c/${enc(cid())}/servers`}
                    activeClass="text-brand-600"
                    class="hover:underline"
                  >
                    Servers
                  </A>
                  <A
                    href={`/c/${enc(cid())}/launch`}
                    activeClass="text-brand-600"
                    class="hover:underline"
                  >
                    Launch
                  </A>
                  <A
                    href={`/c/${enc(cid())}/databases`}
                    activeClass="text-brand-600"
                    class="hover:underline"
                  >
                    Databases
                  </A>
                  <A
                    href={`/c/${enc(cid())}/settings`}
                    activeClass="text-brand-600"
                    class="hover:underline"
                  >
                    Settings
                  </A>
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
        <Route
          path="/c/:clusterId/databases/:dbId"
          component={DatabaseDetail}
        />
        <Route
          path="/c/:clusterId/databases/:dbId/tables/:table"
          component={TableView}
        />
        <Route
          path="/c/:clusterId/databases/:dbId/tables/:table/schema"
          component={TableSchema}
        />
        <Route
          path="/c/:clusterId/databases/:dbId/tables/:table/audit"
          component={TableAudit}
        />
        <Route
          path="/c/:clusterId/databases/:dbId/tables/:table/permissions"
          component={TablePermissions}
        />
        <Route
          path="/c/:clusterId/databases/:dbId/tables/:table/import-export"
          component={TableImportExport}
        />
        <Route path="/c/:clusterId/settings" component={Settings} />
        <Route path="/deploy" component={Deploy} />
        {/* Home when no cluster */}
        <Route path="/" component={Home} />
      </Route>
    </Router>
  )
}

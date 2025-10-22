import { Tabs } from '../components/Tabs'
import Card from '../components/Card'
import StatusPill from '../components/StatusPill'
import KeyValueList from '../components/KeyValueList'
import LogViewer from '../components/LogViewer'
import { useParams, useNavigate } from '@solidjs/router'
import {
  Show,
  createEffect,
  createMemo,
  createResource,
  createSignal,
  onCleanup,
  onMount
} from 'solid-js'
import { getServer, getClusterWorkspace, getClusterWorkspaceLogs, deleteClusterWorkspace } from '../lib/api'
import { formatDate } from '../lib/format'
import { apiUrl } from '../lib/config'
import { ringBuffer, WSManager } from '../lib/ws'

export default function ServerDetail() {
  const params = useParams()
  const navigate = useNavigate()
  const clusterId = () => params.clusterId || ''
  const isClusterScoped = () => !!clusterId()
  const serverId = () => params.id

  // Cluster-scoped workspace fetch
  const [ws] = createResource(
    () => (isClusterScoped() ? `${clusterId()}::${serverId()}` : null),
    async (key) => {
      const [cid, name] = (key || '').split('::')
      if (!cid || !name) return null
      return await getClusterWorkspace(cid, name)
    }
  )

  // Global server fetch (legacy)
  const [srv] = createResource(
    () => (!isClusterScoped() ? serverId() : null),
    (id: string) => getServer(id)
  )

  // Normalize details
  const details = createMemo(() => {
    if (isClusterScoped()) {
      const obj = ws() as any
      if (!obj) return null
      const meta = (obj?.metadata || {}) as any
      const spec = (obj?.spec || {}) as any
      const status = (obj?.status || {}) as any
      const name = String(meta?.name || '')
      const image = String(spec?.image || '')
      const phase = String(status?.phase || '')
      const rr = Number(status?.readyReplicas || 0)
      const st = phase === 'Running' && rr > 0 ? 'running' : (phase?.toLowerCase() || 'pending')
      const created = meta?.creationTimestamp ? new Date(meta.creationTimestamp).toISOString() : undefined
      const ports = Array.isArray(spec?.ports)
        ? spec.ports.map((p: any) => ({ name: String(p?.name || ''), port: Number(p?.containerPort || p?.port || 0) })).filter((p: any) => p.port > 0)
        : []
      const envArr = Array.isArray(spec?.env) ? spec.env : []
      const env: Record<string, string> = {}
      for (const e of envArr) {
        if (e && e.name) env[e.name] = String(e.value ?? '')
      }
      return {
        id: name,
        name,
        image,
        status: st,
        node: undefined,
        created_at: created,
        updated_at: undefined,
        ports,
        resources: spec?.resources,
        args: spec?.args || [],
        env
      }
    }
    const s = srv()
    return s
  })

  // IDE URL for legacy and cluster-scoped
  const ideUrl = createMemo(() => {
    const id = serverId()
    if (!id) return ''
    if (isClusterScoped()) {
      const cid = clusterId()
      if (!cid) return ''
      return apiUrl(`/api/cluster/${encodeURIComponent(cid)}/proxy/server/${encodeURIComponent(id)}/`)
    }
    const s = srv()
    if (s?.url) return s.url
    return apiUrl(`/proxy/server/${encodeURIComponent(id)}/`)
  })

  // IDE preflight (works for both legacy and cluster-scoped)
  const [frameSrc, setFrameSrc] = createSignal<string | null>(null)
  const [ideChecking, setIdeChecking] = createSignal(false)
  const [ideError, setIdeError] = createSignal<string | null>(null)

  createEffect(() => {
    const url = ideUrl()
    const active = tab() === 'ide' && !!url
    if (!active) {
      setFrameSrc(null)
      setIdeChecking(false)
      setIdeError(null)
      return
    }

    let cancelled = false
    setIdeChecking(true)
    setIdeError(null)

    const controller = new AbortController()
    const start = Date.now()

    const rand = Math.random().toString(36).slice(2, 10)

    const check = async (attempt = 0) => {
      if (cancelled) return
      try {
        const res = await fetch(url, {
          method: 'GET',
          credentials: 'include',
          cache: 'no-store',
          headers: {
            Pragma: 'no-cache',
            'Cache-Control': 'no-cache',
            Accept: 'text/html'
          },
          signal: controller.signal
        })
        if (!res.ok) {
          console.warn('IDE preflight non-OK', {
            url,
            status: res.status,
            statusText: res.statusText,
            attempt
          })
        }
        if (res.ok || (res.status >= 200 && res.status < 400)) {
          const q = url.includes('?') ? '&' : '?'
          setFrameSrc(`${url}${q}xid=${rand}`)
          setIdeChecking(false)
          return
        }
      } catch (e) {
        console.warn('IDE preflight error', { url, error: e, attempt })
        if (cancelled) return
      }
      const elapsed = Date.now() - start
      const backoff = Math.min(1500 + attempt * 250, 2500)
      if (elapsed > 15000) {
        setIdeError(
          'IDE is taking longer than expected to start. You can retry switching tabs or wait a moment.'
        )
        setIdeChecking(false)
        return
      }
      setTimeout(() => check(attempt + 1), backoff)
    }

    check(0)

    onCleanup(() => {
      cancelled = true
      controller.abort()
    })
  })

  // Cluster-scoped logs SSE
  const [wsLogs, setWsLogs] = createSignal<Array<{ t: string; msg: string }>>([])
  let sse: WSManager | undefined
  let logBuf = ringBuffer<{ t: string; msg: string }>(2000)

  // Only open SSE when cluster-scoped and Logs tab is active; ensure single connection
  createEffect(() => {
    const cid = clusterId()
    const name = serverId()
    const wantOpen = isClusterScoped() && tab() === 'logs' && !!cid && !!name

    // Close any existing stream if not wanted
    if (!wantOpen) {
      sse?.close()
      sse = undefined
      return
    }

    // Start a fresh stream for current cid/name
    sse?.close()
    logBuf = ringBuffer<{ t: string; msg: string }>(2000)
    setWsLogs([])

    const url = apiUrl(`/api/cluster/${encodeURIComponent(cid)}/workspaces/${encodeURIComponent(name)}/logs/stream`)
    sse = new WSManager(url)
    const offState = sse.on('state', () => {})
    const offMsg = sse.on('message', (obj: any) => {
      if (obj && obj.t && obj.msg) {
        logBuf.push({ t: obj.t, msg: obj.msg })
        setWsLogs([...logBuf.get()])
      }
    })
    sse.open()

    onCleanup(() => {
      offState()
      offMsg()
      sse?.close()
      sse = undefined
    })
  })

  const [tab, setTab] = createSignal<'info' | 'debug' | 'error' | 'ide' | 'logs'>(isClusterScoped() ? 'logs' : 'info')

  return (
    <div class="flex flex-col gap-4">
      <Show when={details()} fallback={<div>Loading…</div>}>
        {(server) => (
          <>
            <Card
              title={server().name}
              actions={
                <div class="flex items-center gap-2">
                  <StatusPill status={server().status as any} />
                  <Show when={isClusterScoped()}>
                    <button
                      class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-red-50 dark:bg-neutral-800 hover:bg-red-100 dark:hover:bg-neutral-700 text-red-700"
                      onClick={async () => {
                        const name = server().name
                        const ok = window.confirm(`Shut down server “${name}”? This deletes the workspace.`)
                        if (!ok) return
                        const done = await deleteClusterWorkspace(clusterId(), name)
                        if (!done) {
                          window.alert('Failed to shut down server.')
                          return
                        }
                        navigate(`/c/${encodeURIComponent(clusterId())}/servers`)
                      }}
                    >
                      Shutdown
                    </button>
                  </Show>
                </div>
              }
            >
              <div class="grid sm:grid-cols-2 gap-4">
                <div>
                  <div class="text-sm">
                    <span class="text-neutral-500">Image:</span>{' '}
                    {server().image || '-'}
                  </div>
                  <div class="text-sm">
                    <span class="text-neutral-500">Node:</span>{' '}
                    {server().node ?? '-'}
                  </div>
                  <div class="text-sm">
                    <span class="text-neutral-500">Created:</span>{' '}
                    {server().created_at ? formatDate(server().created_at) : '-'}
                  </div>
                  <div class="text-sm">
                    <span class="text-neutral-500">Updated:</span>{' '}
                    {server().updated_at ? formatDate(server().updated_at) : '-'}
                  </div>
                  <div class="text-sm">
                    <span class="text-neutral-500">Ports:</span>{' '}
                    {(server().ports ?? [])
                      .map((p: any) => `${p.name ?? ''}:${p.port}`)
                      .join(', ') || '-'}
                  </div>
                </div>
                <div>
                  <div class="text-sm">
                    <span class="text-neutral-500">Resources:</span>{' '}
                    {server()?.resources
                      ? `${server()?.resources?.cpu ?? ''} ${server()?.resources?.memory ?? ''}`
                      : '-'}
                  </div>
                  <div class="mt-2">
                    <div class="text-xs text-neutral-500">Args</div>
                    <div class="text-sm">
                      {(server().args ?? []).join(' ') || '—'}
                    </div>
                  </div>
                  <div class="mt-2">
                    <div class="text-xs text-neutral-500">Env</div>
                    <KeyValueList data={server().env ?? {}} />
                  </div>
                </div>
              </div>
            </Card>

            <Card title="Logs & Tools">
              <Tabs
                tabs={
                  (
                    isClusterScoped()
                      ? [
                          { id: 'logs', label: 'Logs' },
                          ...(ideUrl() ? [{ id: 'ide', label: 'IDE' }] : [])
                        ]
                      : [
                          { id: 'info', label: 'Info' },
                          { id: 'debug', label: 'Debug' },
                          { id: 'error', label: 'Error' },
                          ...(ideUrl() ? [{ id: 'ide', label: 'IDE' }] : [])
                        ]
                  ) as { id: string; label: string }[]
                }
                value={tab()}
                onChange={(t) => setTab(t as any)}
              />
              {isClusterScoped() ? (
                tab() === 'logs' ? (
                  <div class="flex flex-col gap-2">
                    <div class="h-80 overflow-auto rounded border bg-neutral-950 text-neutral-100 font-mono text-xs p-2">
                      {wsLogs().length === 0 ? (
                        <div class="text-neutral-400">No logs yet…</div>
                      ) : (
                        wsLogs().map((l) => (
                          <div>{`${l.t} ${l.msg}`}</div>
                        ))
                      )}
                    </div>
                    <div class="text-xs text-neutral-500">Live logs</div>
                  </div>
                ) : (
                  // IDE tab for cluster-scoped
                  <Show
                    when={ideUrl()}
                    fallback={
                      <div class="text-sm text-neutral-500">
                        IDE not available.
                      </div>
                    }
                  >
                    {(url) => {
                      const [loaded, setLoaded] = createSignal(false)
                      return (
                        <div class="relative border rounded-md overflow-hidden h-[70vh]">
                          <div class="absolute top-2 right-2 z-10">
                            <a href={frameSrc() || url()} target="_blank" rel="noreferrer" class="btn btn-sm">Open in new tab</a>
                          </div>
                          <iframe
                            src={frameSrc() || ''}
                            title="code-server"
                            class="w-full h-full bg-white"
                            referrerpolicy="no-referrer"
                            allow="clipboard-read; clipboard-write;"
                            onLoad={() => setLoaded(true)}
                          />
                          <Show when={!loaded() || ideChecking()}>
                            <div class="absolute inset-0 flex items-center justify-center bg-white/70 text-sm text-neutral-600">
                              {ideError() || 'Opening IDE…'}
                            </div>
                          </Show>
                        </div>
                      )
                    }}
                  </Show>
                )
              ) : (
                <>
                  {tab() === 'info' && (
                    <LogViewer serverId={details()!.id} level="info" />
                  )}
                  {tab() === 'debug' && (
                    <LogViewer serverId={details()!.id} level="debug" />
                  )}
                  {tab() === 'error' && (
                    <LogViewer serverId={details()!.id} level="error" />
                  )}
                  {tab() === 'ide' && (
                    <Show
                      when={ideUrl()}
                      fallback={
                        <div class="text-sm text-neutral-500">
                          IDE not available.
                        </div>
                      }
                    >
                      {(url) => {
                        const [loaded, setLoaded] = createSignal(false)
                        return (
                          <div class="relative border rounded-md overflow-hidden h-[70vh]">
                            <div class="absolute top-2 right-2 z-10">
                              <a href={frameSrc() || url()} target="_blank" rel="noreferrer" class="btn btn-sm">Open in new tab</a>
                            </div>
                            <iframe
                              src={frameSrc() || ''}
                              title="code-server"
                              class="w-full h-full bg-white"
                              referrerpolicy="no-referrer"
                              allow="clipboard-read; clipboard-write;"
                              onLoad={() => setLoaded(true)}
                            />
                            <Show when={!loaded() || ideChecking()}>
                              <div class="absolute inset-0 flex items-center justify-center bg-white/70 text-sm text-neutral-600">
                                {ideError() || 'Opening IDE…'}
                              </div>
                            </Show>
                          </div>
                        )
                      }}
                    </Show>
                  )}
                </>
              )}
            </Card>

            <Card title="Events">
              <div class="text-sm text-neutral-500">
                No events stream connected.
              </div>
            </Card>
          </>
        )}
      </Show>
    </div>
  )
}

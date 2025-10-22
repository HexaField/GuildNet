import {
  For,
  Show,
  createEffect,
  createMemo,
  createResource,
  createSignal,
  onCleanup
} from 'solid-js'
import Table from '../components/Table'
import StatusPill from '../components/StatusPill'
import Card from '../components/Card'
import Input from '../components/Input'
import { A, useParams } from '@solidjs/router'
import { deleteClusterWorkspace, listClusterServers } from '../lib/api'
import { timeAgo } from '../lib/format'
import type { Server } from '../lib/types'

const intervals = [5000, 10000, 30000, 0] as const

export default function Servers() {
  const params = useParams()
  const clusterId = () => params.clusterId || ''
  const [search, setSearch] = createSignal('')
  const [status, setStatus] = createSignal<string>('')
  const [period, setPeriod] = createSignal<(typeof intervals)[number]>(10000)
  const [servers, { refetch }] = createResource<Server[], string>(
    clusterId,
    async (cid: string) => listClusterServers(cid)
  )

  let timer: number | undefined
  createEffect(() => {
    if (timer) window.clearInterval(timer)
    if (period() > 0) timer = window.setInterval(() => refetch(), period())
    onCleanup(() => {
      if (timer) window.clearInterval(timer)
    })
  })

  const filtered = createMemo<Server[]>(() => {
    const q = search().toLowerCase().trim()
    const s = status()
    return (servers() ?? []).filter(
      (srv: Server) =>
        (!q ||
          srv.name.toLowerCase().includes(q) ||
          srv.image.toLowerCase().includes(q)) &&
        (!s || srv.status === (s as any))
    )
  })

  const headers = ['Name', 'Image', 'Status', 'Node', 'Age', 'Ports', 'Actions']
  const rows = () =>
    filtered().map((srv: Server) => (
      <tr class="border-b last:border-0">
        <td class="py-2 pr-4">
          <div class="font-medium">{srv.name}</div>
          <div class="text-xs text-neutral-500">{srv.id}</div>
        </td>
        <td class="py-2 pr-4">{srv.image}</td>
        <td class="py-2 pr-4">
          <StatusPill status={srv.status} />
        </td>
        <td class="py-2 pr-4">
          <div>{srv.node ?? '-'}</div>
          <Show when={srv.machineName || (srv.tailnetIPs && srv.tailnetIPs.length > 0)}>
            <div class="text-xs text-neutral-500">
              <Show when={srv.machineName}>
                <span>{srv.machineName}</span>
              </Show>
              <Show when={srv.tailnetIPs && srv.tailnetIPs.length > 0}>
                <span>
                  {' '}
                  ({(srv.tailnetIPs || []).join(', ')})
                </span>
              </Show>
            </div>
          </Show>
        </td>
        <td class="py-2 pr-4">{timeAgo(srv.created_at)}</td>
        <td class="py-2 pr-4">
          {(srv.ports ?? []).map((p) => `${p.name ?? ''}:${p.port}`).join(', ')}
        </td>
        <td class="py-2 pr-4">
          <A
            class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
            href={`/c/${encodeURIComponent(clusterId())}/servers/${encodeURIComponent(srv.id)}`}
          >
            Inspect
          </A>
          <button
            class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border ml-2 bg-red-50 dark:bg-neutral-800 hover:bg-red-100 dark:hover:bg-neutral-700 text-red-700"
            onClick={async () => {
              const ok = window.confirm(`Shut down server “${srv.name}”? This deletes the workspace.`)
              if (!ok) return
              const done = await deleteClusterWorkspace(clusterId(), srv.name)
              if (!done) {
                window.alert('Failed to shut down server.')
                return
              }
              refetch()
            }}
          >
            Shutdown
          </button>
        </td>
      </tr>
    ))

  return (
    <div class="flex flex-col gap-4">
      <Card
        title="Servers"
        actions={
          <div class="flex items-center gap-2">
            <Input
              id="servers-search"
              class="w-64"
              placeholder="Search name or image"
              value={search()}
              onInput={(e) => setSearch(e.currentTarget.value)}
            />
            <select
              class="w-36 rounded-md border px-3 py-2 bg-white dark:bg-neutral-900"
              value={status()}
              onChange={(e) => setStatus(e.currentTarget.value)}
            >
              <option value="">All statuses</option>
              <option value="running">Running</option>
              <option value="pending">Pending</option>
              <option value="failed">Failed</option>
              <option value="stopped">Stopped</option>
            </select>
            <select
              class="w-32 rounded-md border px-3 py-2 bg-white dark:bg-neutral-900"
              value={String(period())}
              onChange={(e) => setPeriod(Number(e.currentTarget.value) as any)}
            >
              <option value="5000">5s</option>
              <option value="10000">10s</option>
              <option value="30000">30s</option>
              <option value="0">Off</option>
            </select>
            <button
              class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
              onClick={() => refetch()}
            >
              Refresh
            </button>
          </div>
        }
      >
        <Show when={servers.state === 'ready'} fallback={<div>Loading…</div>}>
          {filtered().length === 0 ? (
            <div class="text-sm text-neutral-500">No servers found.</div>
          ) : (
            <Table headers={headers} rows={rows()} />
          )}
        </Show>
      </Card>
    </div>
  )
}

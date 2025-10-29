import { createMemo, createResource, createSignal, For, Show } from 'solid-js'
import { useParams } from '@solidjs/router'
import { apiUrl } from '../lib/config'
import { pushToast } from '../components/Toaster'

type Audit = { id: string; action: string; ts: string; diff?: any }

async function fetchAudit(clusterId: string, db: string): Promise<Audit[]> {
  try {
    const r = await fetch(
      apiUrl(
        `/api/cluster/${encodeURIComponent(clusterId)}/db/${encodeURIComponent(db)}/audit`
      )
    )
    if (!r.ok) return []
    return await r.json()
  } catch {
    return []
  }
}

export default function TableAudit() {
  const params = useParams()
  const [events] = createResource(
    () => [params.clusterId!, params.dbId!] as [string, string],
    ([c, d]) => fetchAudit(c, d)
  )
  const [q, setQ] = createSignal('')
  const [action, setAction] = createSignal('')
  const filtered = createMemo(() => {
    const list = events() || []
    const a = action()
    const s = q().toLowerCase()
    return list.filter(
      (ev) =>
        (!a || ev.action === a) &&
        (!s || JSON.stringify(ev).toLowerCase().includes(s))
    )
  })
  const restoreRow = async (e: Audit) => {
    try {
      const parts = e.id.split('/')
      if (parts.length < 3) throw new Error('unsupported id')
      const table = parts[0]
      const rowId = parts[1]
      if (e.action === 'update' && e.diff && typeof e.diff === 'object') {
        const res = await fetch(
          apiUrl(
            `/api/cluster/${encodeURIComponent(params.clusterId || '')}/db/${encodeURIComponent(String(params.dbId || ''))}/tables/${encodeURIComponent(String(table))}/rows/${encodeURIComponent(String(rowId))}`
          ),
          {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(e.diff)
          }
        )
        if (!res.ok) throw new Error(`${res.status}`)
        pushToast({ type: 'success', message: 'Row restored' })
      } else if (
        e.action === 'delete' &&
        e.diff &&
        typeof e.diff === 'object'
      ) {
        const res = await fetch(
          apiUrl(
            `/api/cluster/${encodeURIComponent(params.clusterId || '')}/db/${encodeURIComponent(String(params.dbId || ''))}/tables/${encodeURIComponent(String(table))}/rows`
          ),
          {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ rows: e.diff })
          }
        )
        if (!res.ok) throw new Error(`${res.status}`)
        pushToast({ type: 'success', message: 'Row reinserted' })
      } else {
        pushToast({
          type: 'info',
          message: 'Restore not available for this event'
        })
      }
    } catch (err: any) {
      pushToast({
        type: 'error',
        message: `Restore failed: ${err?.message || ''}`
      })
    }
  }
  const restoreSchema = async (e: Audit) => {
    try {
      if (
        e.action !== 'update_schema' ||
        !e.diff ||
        typeof e.diff !== 'object'
      ) {
        pushToast({ type: 'info', message: 'No schema to restore' })
        return
      }
      const parts = e.id.split('/')
      const table = parts[0]
      const body = JSON.stringify(e.diff)
      const res = await fetch(
        apiUrl(
          `/api/cluster/${encodeURIComponent(params.clusterId || '')}/db/${encodeURIComponent(String(params.dbId || ''))}/tables/${encodeURIComponent(String(table))}`
        ),
        {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body
        }
      )
      if (!res.ok) throw new Error(`${res.status}`)
      pushToast({ type: 'success', message: 'Schema restored' })
    } catch (err: any) {
      pushToast({
        type: 'error',
        message: `Restore failed: ${err?.message || ''}`
      })
    }
  }
  return (
    <div class="space-y-4">
      <h1 class="text-xl font-semibold">Audit (recent)</h1>
      <div class="flex items-center gap-2 text-xs">
        <input
          class="border rounded px-2 py-1"
          placeholder="filter text"
          value={q()}
          onInput={(e) => setQ(e.currentTarget.value)}
        />
        <select
          class="border rounded px-2 py-1"
          value={action()}
          onChange={(e) => setAction(e.currentTarget.value)}
        >
          <option value="">all actions</option>
          <option value="create_table">create_table</option>
          <option value="update_schema">update_schema</option>
          <option value="insert">insert</option>
          <option value="update">update</option>
          <option value="delete">delete</option>
        </select>
      </div>
      <div class="border rounded divide-y bg-white dark:bg-neutral-900 text-sm">
        <For each={filtered()}>
          {(e) => (
            <div
              class={`p-2 space-y-1 ${e.action.includes('schema') ? 'bg-amber-50 dark:bg-amber-900/20' : ''}`}
            >
              <div class="flex items-center justify-between">
                <span class="font-mono text-xs">{e.id}</span>
                <span class="text-xs text-neutral-500">{e.ts}</span>
              </div>
              <div class="text-xs flex items-center gap-2">
                <span class="font-semibold">{e.action}</span>
                {e.action.includes('schema') && (
                  <span class="text-[10px] px-1.5 py-0.5 rounded bg-amber-100 text-amber-800">
                    schema
                  </span>
                )}
              </div>
              {e.diff && (
                <pre class="bg-neutral-100 dark:bg-neutral-800 p-2 rounded overflow-auto text-[10px] leading-snug max-h-40">
                  {JSON.stringify(e.diff, null, 2)}
                </pre>
              )}
              <Show when={e.action === 'update' || e.action === 'delete'}>
                <button
                  class="text-xs border rounded px-2 py-1"
                  onClick={() => restoreRow(e)}
                >
                  Restore
                </button>
              </Show>
              <Show when={e.action === 'update_schema'}>
                <button
                  class="text-xs border rounded px-2 py-1"
                  onClick={() => restoreSchema(e)}
                >
                  Restore schema
                </button>
              </Show>
            </div>
          )}
        </For>
        {events.loading && <div class="p-2 text-xs">Loading…</div>}
      </div>
    </div>
  )
}

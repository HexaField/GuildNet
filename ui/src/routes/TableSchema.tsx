import { createResource, createSignal, For } from 'solid-js'
import { useParams } from '@solidjs/router'
import Input from '../components/Input'
import Button from '../components/Button'
import { apiUrl } from '../lib/config'
import { pushToast } from '../components/Toaster'

type ColumnDef = {
  name: string
  type: string
  required?: boolean
  mask?: boolean
}
type TableMeta = { name: string; primary_key: string; schema: ColumnDef[] }

async function fetchTable(
  clusterId: string,
  dbId: string,
  table: string
): Promise<TableMeta | null> {
  try {
    const r = await fetch(
      apiUrl(
        `/api/cluster/${encodeURIComponent(clusterId)}/db/${encodeURIComponent(dbId)}/tables/${encodeURIComponent(table)}`
      )
    )
    if (!r.ok) return null
    return await r.json()
  } catch {
    return null
  }
}

export default function TableSchema() {
  const params = useParams()
  const [meta, { refetch }] = createResource(
    () =>
      [
        params.clusterId as string,
        params.dbId as string,
        params.table as string
      ] as [string, string, string],
    ([c, d, t]) => fetchTable(c, d, t)
  )
  const [editing, setEditing] = createSignal<ColumnDef[]>([])
  const [pk, setPk] = createSignal('id')
  const [dirty, setDirty] = createSignal(false)
  const startEdit = () => {
    if (meta()) {
      setEditing(meta()!.schema.map((c) => ({ ...c })))
      setPk(meta()!.primary_key || 'id')
      setDirty(false)
    }
  }
  const addCol = () => {
    setEditing((cols) => [...cols, { name: '', type: 'string' }])
    setDirty(true)
  }
  const updateCol = (i: number, patch: Partial<ColumnDef>) => {
    setEditing((cols) =>
      cols.map((c, idx) => (idx === i ? { ...c, ...patch } : c))
    )
    setDirty(true)
  }
  const removeCol = (i: number) => {
    setEditing((cols) => cols.filter((_, idx) => idx !== i))
    setDirty(true)
  }
  const cancel = () => {
    setEditing([])
    setDirty(false)
  }
  const save = async () => {
    try {
      const res = await fetch(
        apiUrl(
          `/api/cluster/${encodeURIComponent(params.clusterId!)}/db/${encodeURIComponent(params.dbId!)}/tables/${encodeURIComponent(
            params.table!
          )}`
        ),
        {
          method: 'PATCH',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ schema: editing(), primary_key: pk() })
        }
      )
      if (!res.ok) throw new Error(`${res.status}`)
      pushToast({ type: 'success', message: 'Schema saved' })
    } catch (e: any) {
      pushToast({ type: 'error', message: `Save failed: ${e?.message || ''}` })
    }
    setEditing([])
    setDirty(false)
    refetch()
  }
  return (
    <div class="space-y-4">
      <div class="flex items-center justify-between">
        <h1 class="text-xl font-semibold">Schema: {params.table}</h1>
        {!editing().length && (
          <Button onClick={startEdit} variant="primary">
            Edit Schema
          </Button>
        )}
        {editing().length > 0 && (
          <div class="flex gap-2">
            <Button onClick={cancel}>Cancel</Button>
            <Button variant="primary" disabled={!dirty()} onClick={save}>
              Save
            </Button>
          </div>
        )}
      </div>
      {meta.loading && <div class="p-2 text-sm">Loading…</div>}
      {!meta.loading && meta() && !editing().length && (
        <table class="text-sm min-w-full border rounded">
          <thead>
            <tr class="bg-neutral-50 dark:bg-neutral-800">
              <th class="px-2 py-1 text-left">Column</th>
              <th class="px-2 py-1 text-left">Type</th>
              <th class="px-2 py-1">Req</th>
              <th class="px-2 py-1">Mask</th>
            </tr>
          </thead>
          <tbody>
            <For each={meta()!.schema}>
              {(c) => (
                <tr class="border-t">
                  <td class="px-2 py-1">{c.name}</td>
                  <td class="px-2 py-1">{c.type}</td>
                  <td class="px-2 py-1 text-center">{c.required ? '✓' : ''}</td>
                  <td class="px-2 py-1 text-center">{c.mask ? '✓' : ''}</td>
                </tr>
              )}
            </For>
          </tbody>
        </table>
      )}
      {editing().length > 0 && (
        <div class="space-y-4">
          <div>
            <label class="block text-xs font-medium mb-1">Primary Key</label>
            <Input
              value={pk()}
              onInput={(e) => {
                setPk(e.currentTarget.value)
                setDirty(true)
              }}
            />
          </div>
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <h3 class="text-xs font-semibold uppercase">Columns</h3>
              <Button class="!px-2 !py-1 text-xs" onClick={addCol}>
                Add
              </Button>
            </div>
            <div class="space-y-2">
              <For each={editing()}>
                {(col, i) => (
                  <div class="grid grid-cols-12 gap-2 items-center">
                    <div class="col-span-4">
                      <Input
                        value={col.name}
                        onInput={(e) =>
                          updateCol(i(), { name: e.currentTarget.value })
                        }
                        placeholder="column"
                      />
                    </div>
                    <div class="col-span-3">
                      <select
                        class="w-full rounded-md border px-2 py-2 bg-white dark:bg-neutral-900 text-sm"
                        value={col.type}
                        onChange={(e) =>
                          updateCol(i(), { type: e.currentTarget.value })
                        }
                      >
                        <option value="string">string</option>
                        <option value="number">number</option>
                        <option value="boolean">boolean</option>
                        <option value="timestamp">timestamp</option>
                        <option value="json">json</option>
                      </select>
                    </div>
                    <div class="col-span-2 flex items-center gap-2 text-xs">
                      <label class="flex items-center gap-1">
                        <input
                          type="checkbox"
                          checked={col.required}
                          onChange={(e) =>
                            updateCol(i(), {
                              required: e.currentTarget.checked
                            })
                          }
                        />{' '}
                        req
                      </label>
                      <label class="flex items-center gap-1">
                        <input
                          type="checkbox"
                          checked={col.mask}
                          onChange={(e) =>
                            updateCol(i(), { mask: e.currentTarget.checked })
                          }
                        />{' '}
                        mask
                      </label>
                    </div>
                    <div class="col-span-2 flex justify-end">
                      <button
                        class="text-xs text-red-500 hover:underline"
                        onClick={() => removeCol(i())}
                      >
                        Remove
                      </button>
                    </div>
                  </div>
                )}
              </For>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

import { createResource, For, createSignal } from 'solid-js'
import { useParams, A } from '@solidjs/router'
import Button from '../components/Button'
import Modal from '../components/Modal'
import Input from '../components/Input'
import {
  createClusterTable,
  listClusterTables,
  type TableDef,
  type ColumnDef
} from '../lib/api'

async function fetchTables(
  clusterId: string,
  dbId: string
): Promise<TableDef[]> {
  return listClusterTables(clusterId, dbId)
}

export default function DatabaseDetail() {
  const params = useParams()
  const clusterId = () => params.clusterId!
  const dbId = () => params.dbId!
  const [tables, { refetch }] = createResource(
    () => [clusterId(), dbId()] as [string, string],
    ([c, d]) => fetchTables(c as string, d as string)
  )
  const [open, setOpen] = createSignal(false)
  const [name, setName] = createSignal('')
  const [pk, setPk] = createSignal('id')
  const [columns, setColumns] = createSignal<ColumnDef[]>([
    { name: 'id', type: 'string', required: true }
  ])
  const [creating, setCreating] = createSignal(false)
  const addColumn = () =>
    setColumns((cols) => [...cols, { name: '', type: 'string' }])
  const updateCol = (idx: number, patch: Partial<ColumnDef>) =>
    setColumns((cols) =>
      cols.map((c, i) => (i === idx ? { ...c, ...patch } : c))
    )
  const removeCol = (idx: number) =>
    setColumns((cols) => cols.filter((_, i) => i !== idx))
  const submit = async () => {
    if (!name().trim()) return
    setCreating(true)
    await createClusterTable(clusterId(), dbId(), {
      name: name().trim(),
      primary_key: pk().trim() || undefined,
      schema: columns()
    })
    setCreating(false)
    setOpen(false)
    setName('')
    setPk('id')
    setColumns([{ name: 'id', type: 'string', required: true }])
    refetch()
  }
  return (
    <div class="space-y-4">
      <div class="flex items-center justify-between">
        <h1 class="text-xl font-semibold">Database: {dbId()}</h1>
        <Button variant="primary" onClick={() => setOpen(true)}>
          New Table
        </Button>
      </div>
      <div class="border rounded-lg divide-y overflow-hidden bg-white dark:bg-neutral-900">
        <For each={tables()}>
          {(t) => (
            <A
              href={`/c/${encodeURIComponent(clusterId())}/databases/${encodeURIComponent(dbId())}/tables/${encodeURIComponent(t.name)}`}
              class="flex items-center px-4 py-3 hover:bg-neutral-50 dark:hover:bg-neutral-800"
            >
              <div class="flex-1">
                <div class="font-medium">{t.name}</div>
                <div class="text-xs text-neutral-500">
                  PK: {t.primary_key || 'id'}
                </div>
              </div>
            </A>
          )}
        </For>
        {tables.loading && <div class="p-4 text-sm">Loading…</div>}
        {tables() && tables()!.length === 0 && !tables.loading && (
          <div class="p-4 text-sm text-neutral-500">No tables yet.</div>
        )}
      </div>
      <Modal
        title="Create Table"
        open={open()}
        onClose={() => setOpen(false)}
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button
              variant="primary"
              disabled={creating() || !name().trim()}
              onClick={submit}
            >
              {creating() ? 'Creating…' : 'Create'}
            </Button>
          </>
        }
      >
        <div class="space-y-4">
          <div class="grid gap-4">
            <div>
              <label class="block text-xs font-medium mb-1">Name</label>
              <Input
                value={name()}
                onInput={(e) => setName(e.currentTarget.value)}
                placeholder="events"
              />
            </div>
            <div>
              <label class="block text-xs font-medium mb-1">Primary Key</label>
              <Input
                value={pk()}
                onInput={(e) => setPk(e.currentTarget.value)}
              />
            </div>
          </div>
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <h3 class="text-xs font-semibold uppercase tracking-wide">
                Columns
              </h3>
              <Button class="!px-2 !py-1 text-xs" onClick={addColumn}>
                Add
              </Button>
            </div>
            <div class="space-y-2">
              <For each={columns()}>
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
                    <div class="col-span-2 flex items-center gap-1 text-xs">
                      <label class="flex items-center gap-1 cursor-pointer">
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
                      <label class="flex items-center gap-1 cursor-pointer">
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
                        class="text-xs text-red-500 hover:underline disabled:opacity-40"
                        disabled={i() === 0 && col.name === 'id'}
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
          <p class="text-xs text-neutral-500">
            Schema is stored with the table; columns can be edited later in the
            schema editor.
          </p>
        </div>
      </Modal>
    </div>
  )
}

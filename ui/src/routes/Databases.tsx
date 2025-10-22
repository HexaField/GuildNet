import { createResource, For, createSignal } from 'solid-js'
import { useParams, A } from '@solidjs/router'
import Button from '../components/Button'
import Modal from '../components/Modal'
import Input from '../components/Input'
import {
  createClusterDatabase,
  deleteClusterDatabase,
  listClusterDatabases,
  type DatabaseInstance
} from '../lib/api'
import { pushToast } from '../components/Toaster'

async function fetchDatabases(clusterId: string): Promise<DatabaseInstance[]> {
  return listClusterDatabases(clusterId)
}

export default function Databases() {
  const params = useParams()
  const clusterId = () => params.clusterId!
  const [dbs, { refetch }] = createResource(clusterId, fetchDatabases)
  const [open, setOpen] = createSignal(false)
  const [id, setId] = createSignal('')
  const [name, setName] = createSignal('')
  const [desc, setDesc] = createSignal('')
  const [creating, setCreating] = createSignal(false)

  const submit = async () => {
    if (!id().trim() || !name().trim()) {
      pushToast({ type: 'error', message: 'Database ID and Name are required' })
      return
    }
    setCreating(true)
    try {
      const res = await createClusterDatabase(clusterId(), {
        id: id().trim(),
        name: name().trim(),
        description: desc().trim() || undefined
      })
      if (!res) {
        pushToast({ type: 'error', message: 'Create failed' })
        return
      }
      pushToast({ type: 'success', message: 'Database created' })
      setOpen(false)
      setId('')
      setName('')
      setDesc('')
      refetch()
    } catch (e: any) {
      pushToast({ type: 'error', message: e?.message || 'Create failed' })
    } finally {
      setCreating(false)
    }
  }
  return (
    <div class="space-y-4">
      <div class="flex items-center justify-between">
        <h1 class="text-xl font-semibold">Databases</h1>
        <Button onClick={() => setOpen(true)} variant="primary">
          New Database
        </Button>
      </div>
      <div class="border rounded-lg divide-y overflow-hidden bg-white dark:bg-neutral-900">
        <For each={dbs()}>
          {(d) => {
            const onDelete = async (e: Event) => {
              e.preventDefault()
              e.stopPropagation()
              if (
                !window.confirm(
                  `Delete database ${d.id}? This cannot be undone.`
                )
              )
                return
              const ok = await deleteClusterDatabase(clusterId(), d.id)
              if (ok) {
                pushToast({ type: 'success', message: 'Database deleted' })
                refetch()
              } else {
                pushToast({ type: 'error', message: 'Delete failed' })
              }
            }
            return (
              <A
                href={`/c/${encodeURIComponent(clusterId())}/databases/${encodeURIComponent(d.id)}`}
                class="flex items-center px-4 py-3 hover:bg-neutral-50 dark:hover:bg-neutral-800"
              >
                <div class="flex-1">
                  <div class="font-medium">{d.name}</div>
                  <div class="text-xs text-neutral-500">ID: {d.id}</div>
                </div>
                <div class="flex items-center gap-3">
                  <div class="text-xs text-neutral-500">{d.created_at}</div>
                  <button
                    class="text-xs text-red-600 hover:underline"
                    onClick={onDelete}
                  >
                    Delete
                  </button>
                </div>
              </A>
            )
          }}
        </For>
        {dbs.loading && <div class="p-4 text-sm">Loading…</div>}
        {dbs() && dbs()!.length === 0 && !dbs.loading && (
          <div class="p-4 text-sm text-neutral-500">No databases yet.</div>
        )}
      </div>
      <Modal
        title="Create Database"
        open={open()}
        onClose={() => setOpen(false)}
        footer={
          <>
            <Button
              type="button"
              onClick={() => setOpen(false)}
              class="!bg-neutral-100 dark:!bg-neutral-800"
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant="primary"
              disabled={creating() || name().trim().length === 0}
              onClick={submit}
            >
              {creating() ? 'Creating…' : 'Create'}
            </Button>
          </>
        }
      >
        <div class="space-y-4">
          <div>
            <label class="block text-xs font-medium mb-1">Database ID</label>
            <Input
              value={id()}
              onInput={(e) => setId(e.currentTarget.value)}
              placeholder="analytics"
            />
          </div>
          <div>
            <label class="block text-xs font-medium mb-1">Name</label>
            <Input
              value={name()}
              onInput={(e) => setName(e.currentTarget.value)}
              placeholder="Analytics"
            />
          </div>
          <div>
            <label class="block text-xs font-medium mb-1">Description</label>
            <Input
              value={desc()}
              onInput={(e) => setDesc(e.currentTarget.value)}
              placeholder="Optional description"
            />
          </div>
          <p class="text-xs text-neutral-500">
            Create a new logical database scoped to this cluster.
          </p>
        </div>
      </Modal>
    </div>
  )
}

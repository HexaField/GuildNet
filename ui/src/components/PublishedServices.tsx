import { For, createResource, createSignal } from 'solid-js'
import Card from './Card'
import {
  listPublishedServices,
  deletePublishedService,
  PublishedService
} from '../lib/api'
import { pushToast } from './Toaster'

export default function PublishedServices(props: { clusterId: string }) {
  const clusterId = () => props.clusterId
  const [list, { refetch }] = createResource(clusterId, listPublishedServices)
  const [busy, setBusy] = createSignal(false)

  const revoke = async (svc: PublishedService) => {
    if (!confirm(`Revoke published service ${svc.service}?`)) return
    setBusy(true)
    try {
      const ok = await deletePublishedService(clusterId(), svc.service)
      if (ok) {
        pushToast({ type: 'success', message: 'Revoked' })
        refetch()
      } else {
        pushToast({ type: 'error', message: 'Revoke failed' })
      }
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card title="Published Services">
      <div class="space-y-2">
        <For
          each={list() ?? []}
          fallback={
            <div class="text-xs text-neutral-500">No published services.</div>
          }
        >
          {(p) => (
            <div class="flex items-center justify-between gap-4">
              <div class="text-sm">
                <div class="font-medium">{p.service}</div>
                <div class="text-xs text-neutral-500">
                  {p.addr} • added {new Date(p.added_at).toLocaleString()}
                </div>
              </div>
              <div>
                <button class="btn" disabled={busy()} onClick={() => revoke(p)}>
                  Revoke
                </button>
              </div>
            </div>
          )}
        </For>
      </div>
    </Card>
  )
}

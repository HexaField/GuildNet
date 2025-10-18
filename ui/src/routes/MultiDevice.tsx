import { createResource, createSignal, For, Show } from 'solid-js'
import { listSites, listFederatedServices, getPerSiteStatus } from '../lib/api'

export default function MultiDevice() {
  const [sites] = createResource(listSites)
  const [mdsList, { refetch }] = createResource(listFederatedServices)
  const [selected, setSelected] = createSignal<{ namespace: string; name: string } | null>(null)
  const [status] = createResource(() => selected() ? `${selected()!.namespace}/${selected()!.name}` : null, () => selected() ? getPerSiteStatus(selected()!.namespace, selected()!.name) : Promise.resolve(null))

  return (
    <div class="space-y-4">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-semibold">Federated services</h2>
        <div class="text-sm text-neutral-500">Clusters: {sites()?.length ?? 0}</div>
      </div>

      <div class="grid grid-cols-3 gap-4">
        <div class="col-span-1 bg-white rounded p-3 border">
          <div class="font-medium mb-2">Sites</div>
          <For each={sites() ?? []}>{(s) => (
            <div class="text-sm py-1">{s.name || s.id}</div>
          )}</For>
        </div>
        <div class="col-span-2 bg-white rounded p-3 border">
          <div class="flex items-center justify-between mb-2">
            <div class="font-medium">Services</div>
            <button class="btn" onClick={() => refetch()}>Refresh</button>
          </div>
          <For each={mdsList() ?? []}>{(m) => (
            <div class="p-2 border-b flex items-center gap-2">
              <div class="flex-1">
                <div class="font-medium">{m.name}</div>
                <div class="text-xs text-neutral-500">{m.clusterId} / {m.namespace}</div>
              </div>
              <div>
                <button class="btn" onClick={() => setSelected({ namespace: m.namespace, name: m.name })}>Status</button>
              </div>
            </div>
          )}</For>
          <Show when={selected()}>
            <div class="mt-4 p-2 bg-neutral-50 rounded">
              <div class="font-medium">Per-site status</div>
              <pre class="text-xs mt-2">{JSON.stringify(status(), null, 2)}</pre>
            </div>
          </Show>
        </div>
      </div>
    </div>
  )
}

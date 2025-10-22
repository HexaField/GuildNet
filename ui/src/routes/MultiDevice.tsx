import { createResource, createSignal, For, Show, onCleanup, onMount } from 'solid-js'
import { listSites, listFederatedServices, getPerSiteStatus } from '../lib/api'
import { openSitesStream } from '../lib/ws'
import { timeAgo } from '../lib/format'

export default function MultiDevice(props: { clusterId?: string } = {}) {
  // Use a resource for the site list but update it incrementally from SSE
  const [sites, { refetch: refetchSites, mutate: mutateSites }] = createResource(listSites)
  const stream = openSitesStream()
  onMount(() => {
    stream.open()
    // SSE messages contain a wrapper: { cluster?: string, clusterId?: string, event: { new_val?, old_val? } }
    const offMsg = stream.on('message', (m: any) => {
      try {
        const wrapper = m as any
        const ev = wrapper.event || {}
        const cid = wrapper.clusterId ?? wrapper.cluster
        // Helper: apply upsert of a site record into the current list
        const upsertSite = (site: any) => {
          mutateSites((cur: any[] | undefined) => {
            const arr = (cur || []).slice()
            const id = site.id || site.ID || site.id
            let found = false
            for (let i = 0; i < arr.length; i++) {
              if (arr[i].id === id) {
                arr[i] = { ...arr[i], ...site, clusterId: site.clusterId ?? cid ?? arr[i].clusterId }
                found = true
                break
              }
            }
            if (!found) {
              arr.push({ ...site, clusterId: site.clusterId ?? cid })
            }
            return arr
          })
        }
        const removeSite = (oldVal: any) => {
          mutateSites((cur: any[] | undefined) => {
            if (!cur) return cur
            return cur.filter(s => s.id !== (oldVal.id || oldVal.ID))
          })
        }

        // If new_val exists -> insert/update
        if (ev.new_val) {
          const site = ev.new_val
          upsertSite(site)
          return
        }
        // If only old_val exists -> delete
        if (ev.old_val && !ev.new_val) {
          removeSite(ev.old_val)
          return
        }
        // Fallback: refetch if shape is unexpected
        refetchSites()
      } catch (e) {
        // On any error, fallback to refetch to keep UI correct
        try { refetchSites() } catch {}
      }
    })
    const offErr = stream.on('error', (e: any) => {
      // silently ignore; resource will be refetched on user actions
    })
    onCleanup(() => {
      offMsg()
      offErr()
      stream.close()
    })
  })
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
        <div class="col-span-1 bg-gray rounded p-3 border">
          <div class="font-medium mb-2">Sites</div>
          <For each={(sites() ?? []).filter(s => { if (!props.clusterId) return true; const c = (s as any).cluster || (s as any).clusterId; return c === props.clusterId || s.id === props.clusterId })}>{(s: any) => {
            const name = s.name || s.id
            const tailnet = Array.isArray(s.tailnetIPs) ? s.tailnetIPs : []
            const last = s.lastSeen as string | undefined
            const cpuMilli = typeof s.cpuMilli === 'number' ? s.cpuMilli : undefined
            const memoryMB = typeof s.memoryMB === 'number' ? s.memoryMB : undefined
            const storageMB = typeof s.storageMB === 'number' ? s.storageMB : undefined
            const vramMB = typeof s.vramMB === 'number' ? s.vramMB : undefined
            const supportsCluster = !!s.supportsCluster
            const online = last ? (Date.now() - new Date(last).getTime() < 90_000) : false

            const fmtCPU = (m?: number) => (m == null ? '' : `${(m/1000).toFixed(1)} CPU`)
            const fmtMB = (mb?: number) => {
              if (mb == null) return ''
              if (mb >= 1024) return `${(mb/1024).toFixed(1)} GiB`
              return `${mb} MiB`
            }

            return (
              <div class="py-2 border-b last:border-b-0">
                <div class="flex items-center justify-between">
                  <div class="text-sm font-medium">{name}</div>
                  <div class={`text-[10px] px-2 py-0.5 rounded-full border ${online ? 'bg-green-50 text-green-700 border-green-200' : 'bg-neutral-50 text-neutral-600 border-neutral-200'}`}>{online ? 'online' : 'offline'}</div>
                </div>
                <div class="text-xs text-neutral-500 break-words">{s.id}</div>
                <Show when={tailnet.length > 0}>
                  <div class="text-xs text-neutral-500 mt-0.5">IPs: {tailnet.join(', ')}</div>
                </Show>
                <div class="text-xs text-neutral-500 mt-0.5 flex flex-wrap gap-x-2 gap-y-1">
                  <Show when={cpuMilli != null}><span>{fmtCPU(cpuMilli)}</span></Show>
                  <Show when={memoryMB != null}><span>RAM {fmtMB(memoryMB)}</span></Show>
                  <Show when={storageMB != null}><span>Disk {fmtMB(storageMB)}</span></Show>
                  <Show when={vramMB != null}><span>VRAM {fmtMB(vramMB)}</span></Show>
                </div>
                <div class="text-[11px] text-neutral-400 mt-0.5">
                  <Show when={last} fallback={<span>last seen: —</span>}>
                    last seen: {timeAgo(last)}
                  </Show>
                  <Show when={supportsCluster}>
                    <span class="ml-2 px-1.5 py-0.5 rounded bg-blue-50 text-blue-700 border border-blue-200 text-[10px] align-middle">supports cluster</span>
                  </Show>
                </div>
              </div>
            )
          }}</For>
        </div>
        <div class="col-span-2 bg-gray rounded p-3 border">
          <div class="flex items-center justify-between mb-2">
            <div class="font-medium">Services</div>
            <button class="btn" onClick={() => refetch()}>Refresh</button>
          </div>
          <For each={(mdsList() ?? []).filter((m: any) => { if (!props.clusterId) return true; return m.id === props.clusterId })}>{(m) => (
            <div class="p-2 border-b flex items-center gap-2">
              <div class="flex-1">
                <div class="font-medium">{m.name}</div>
                <div class="text-xs text-neutral-500">{m.id} / {m.namespace}</div>
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

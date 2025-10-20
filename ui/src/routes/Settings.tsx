import { Show, createResource, createSignal, createEffect } from 'solid-js'
import Card from '../components/Card'
import Input from '../components/Input'
import { useNavigate, useParams } from '@solidjs/router'
import {
  attachClusterKubeconfig,
  clusterHealth,
  deleteClusterRecord,
  getClusterKubeconfig,
  getClusterJoinConfig,
  getClusterRecord
} from '../lib/api'
import PublishedServices from '../components/PublishedServices'
import { pushToast } from '../components/Toaster'
import { listSites } from '../lib/api'

function DeviceList(_props: { clusterId: string }) {
  const [sites] = createResource(listSites)
  return (
    <div class="space-y-2">
      <Show when={sites()} fallback={<div class="text-sm text-neutral-500">No devices</div>}>
        {(s) => {
          const list = s().filter((d: any) => {
            // If devices include clusterId, only show those for the current cluster
            if (d.clusterId) return d.clusterId === _props.clusterId
            // Otherwise show cluster-level entries whose id matches cluster
            return d.id === _props.clusterId
          })
          return (
            <div class="grid grid-cols-1 gap-2">
              {list.length === 0 ? (
                <div class="text-sm text-neutral-500">No devices for this cluster</div>
              ) : (
                list.map((d: any) => (
                  <div class="p-2 border rounded bg-white">
                    <div class="flex items-center justify-between">
                      <div class="font-medium">{d.name || d.id}</div>
                      <div class="text-xs text-neutral-500">{d.supportsCluster ? 'supports cluster' : 'hostapp-only'}</div>
                    </div>
                    <div class="text-xs text-neutral-600 mt-1">IPs: {(d.tailnetIPs || []).join(', ') || '-'}</div>
                    <div class="text-xs text-neutral-600 mt-1">CPU: {d.cpuMilli || '-'} m | Mem: {d.memoryMB || '-'} MB</div>
                    <div class="text-xs text-neutral-500 mt-1">Last seen: {d.lastSeen ? String(d.lastSeen) : '-'}</div>
                  </div>
                ))
              )}
            </div>
          )
        }}
      </Show>
    </div>
  )
}

export default function Settings() {
  const params = useParams()
  const navigate = useNavigate()
  const clusterId = () => params.clusterId || ''
  const [cluster] = createResource(clusterId, getClusterRecord)
  const [health, { refetch: refetchHealth }] = createResource(clusterId, clusterHealth)
  const [kubeconfig, setKubeconfig] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [healthDetail, setHealthDetail] = createSignal<{ status: string; code?: string; error?: string } | null>(null)

  const fetchHealthDetail = async () => {
    try {
      const res = await fetch(`/api/deploy/clusters/${encodeURIComponent(clusterId())}?action=health`, { method: 'POST' })
      if (res.ok) {
        const data = await res.json()
        setHealthDetail({ status: String(data?.status || 'unknown'), code: data?.code, error: data?.error })
      } else {
        setHealthDetail({ status: 'unknown' })
      }
    } catch {
      setHealthDetail({ status: 'unknown' })
    }
  }

  // Keep health detail in sync with selected cluster
  createEffect(() => {
    const _ = clusterId()
    fetchHealthDetail()
  })

  const rotateKubeconfig = async () => {
    setBusy(true)
    try {
      if (!kubeconfig().trim()) {
        pushToast({ type: 'error', message: 'Paste a kubeconfig first' })
        return
      }
      const ok = await attachClusterKubeconfig(clusterId(), kubeconfig())
      if (ok) {
        pushToast({ type: 'success', message: 'Kubeconfig attached' })
        setKubeconfig('')
        refetchHealth()
        fetchHealthDetail()
      } else {
        pushToast({ type: 'error', message: 'Attach failed' })
      }
    } finally {
      setBusy(false)
    }
  }

  const downloadKubeconfig = async () => {
    const data = await getClusterKubeconfig(clusterId())
    if (!data) {
      pushToast({ type: 'error', message: 'No kubeconfig stored' })
      return
    }
    const blob = new Blob([data], { type: 'application/x-yaml' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `kubeconfig-${clusterId()}.yaml`
    a.click()
    URL.revokeObjectURL(url)
  }

  const downloadJoinConfig = async () => {
    const data = await getClusterJoinConfig(clusterId())
    if (!data) {
      pushToast({ type: 'error', message: 'No join config available' })
      return
    }
    const blob = new Blob([data], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `guildnet.${clusterId()}.config`
    a.click()
    URL.revokeObjectURL(url)
  }

  const destroyCluster = async () => {
    if (!confirm('Delete this cluster record? This will not touch the actual cluster.')) return
    const ok = await deleteClusterRecord(clusterId())
    if (ok) {
      pushToast({ type: 'success', message: 'Cluster record deleted' })
      navigate('/')
    } else {
      pushToast({ type: 'error', message: 'Delete failed' })
    }
  }

  return (
    <div class="flex flex-col gap-4">
      <Card title="Cluster Settings">
        <Show when={cluster()} fallback={<div>Loading…</div>}>
          {(c) => (
            <div class="space-y-4">
              <div class="grid md:grid-cols-3 gap-4">
                <div>
                  <div class="text-xs text-neutral-500">Cluster ID</div>
                  <div class="font-mono text-sm">{c().id}</div>
                </div>
                <div>
                  <div class="text-xs text-neutral-500">Name</div>
                  <div>{c().name || '-'}</div>
                </div>
                <div>
                  <div class="flex items-center justify-between">
                    <div>
                      <div class="text-xs text-neutral-500">Health</div>
                      <div>{healthDetail()?.status || health() || 'unknown'}</div>
                    </div>
                    <button class="btn" onClick={() => { refetchHealth(); fetchHealthDetail() }}>Refresh</button>
                  </div>
                  <Show when={healthDetail()}>
                    {(h) => (
                      <div class="text-xs text-neutral-500 mt-1">
                        <Show when={h().code}><div>code: {h().code}</div></Show>
                        <Show when={h().error}><div class="text-red-600 dark:text-red-300 break-all">error: {h().error}</div></Show>
                      </div>
                    )}
                  </Show>
                </div>
              </div>

              <div class="space-y-2">
                <label class="block text-sm">
                  Rotate/Attach kubeconfig
                  <textarea class="mt-1 w-full h-36 rounded-md border px-3 py-2 font-mono text-xs" placeholder="Paste kubeconfig YAML" value={kubeconfig()} onInput={(e) => setKubeconfig(e.currentTarget.value)} />
                </label>
                <div class="flex gap-2">
                  <button class="btn" disabled={busy()} onClick={rotateKubeconfig}>Attach</button>
                  <button class="btn" onClick={downloadKubeconfig}>Download stored</button>
                  <button class="btn" onClick={downloadJoinConfig}>Download join config</button>
                </div>
              </div>

              <div class="pt-4 border-t">
                <button class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-red-50 dark:bg-red-900/30 hover:bg-red-100 dark:hover:bg-red-900/50 text-red-700 dark:text-red-200" onClick={destroyCluster}>
                  Delete cluster record
                </button>
              </div>
              <div class="pt-4">
                <PublishedServices clusterId={c().id} />
              </div>
              <div class="pt-4">
                <div class="font-medium mb-2">Connected devices</div>
                <DeviceList clusterId={c().id} />
              </div>
            </div>
          )}
        </Show>
      </Card>
    </div>
  )
}

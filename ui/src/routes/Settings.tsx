import { Show, createResource, createSignal, createEffect } from 'solid-js'
import Card from '../components/Card'
import Input from '../components/Input'
import { useNavigate, useParams } from '@solidjs/router'
import {
  attachClusterKubeconfig,
  deleteClusterRecord,
  getClusterKubeconfig,
  getClusterJoinConfig,
  getClusterOverview,
  listSites,
  getClusterSettings,
  putClusterSettings,
  clusterHealth
} from '../lib/api'
import PublishedServices from '../components/PublishedServices'
import MultiDevice from './MultiDevice'
import { pushToast } from '../components/Toaster'

export default function Settings() {
  const params = useParams()
  const navigate = useNavigate()
  // params.clusterId (route param name) is expected to carry the canonical cluster id (required)
  const clusterId = () => params.clusterId || ''
  const [overview, { refetch: refetchOverview }] = createResource(
    clusterId,
    getClusterOverview
  )
  const [kubeconfig, setKubeconfig] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [healthDetail, setHealthDetail] = createSignal<{
    status: string
    code?: string
    error?: string
  } | null>(null)
  const [settingBusy, setSettingBusy] = createSignal(false)

  const fetchHealthDetail = async () => {
    // If overview contains health info in future, use it; otherwise fall back
    setHealthDetail({ status: String(overview()?.record?.state || 'unknown') })
  }

  // Keep overview in sync with selected cluster
  createEffect(() => {
    const _ = clusterId()
    refetchOverview()
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
        refetchOverview()
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
    if (
      !confirm(
        'Delete this cluster record? This will not touch the actual cluster.'
      )
    )
      return
    const ok = await deleteClusterRecord(clusterId())
    if (ok) {
      pushToast({ type: 'success', message: 'Cluster record deleted' })
      navigate('/')
    } else {
      pushToast({ type: 'error', message: 'Delete failed' })
    }
  }

  const setAPIProxyToTailnet = async () => {
    if (settingBusy()) return
    setSettingBusy(true)
    try {
      const sites = await listSites()
      const cid = clusterId()
      // Prefer self device for this cluster, else any device with tailnet IPs
      const candidates = sites.filter(
        (s) => (s.clusterId === cid || (s as any).cluster === cid)
      )
      const pickIP = (arr?: string[]) =>
        (arr || []).find((x) => /^100\./.test(x)) || (arr || [])[0]
      let ip = ''
      // First, prefer self:true
      for (const s of candidates) {
        if (s.self && s.tailnetIPs && s.tailnetIPs.length > 0) {
          ip = pickIP(s.tailnetIPs) || ''
          if (ip) break
        }
      }
      // Then, any candidate with IPs
      if (!ip) {
        for (const s of candidates) {
          if (s.tailnetIPs && s.tailnetIPs.length > 0) {
            ip = pickIP(s.tailnetIPs) || ''
            if (ip) break
          }
        }
      }
      if (!ip) {
        pushToast({ type: 'error', message: 'No tailnet IPs available yet' })
        return
      }
      const url = `https://${ip}:16443`
      const ok = await putClusterSettings(cid, { api_proxy_url: url })
      if (ok) {
        pushToast({ type: 'success', message: `API proxy set to ${url}` })
        refetchOverview()
      } else {
        pushToast({ type: 'error', message: 'Failed to update settings' })
      }
    } finally {
      setSettingBusy(false)
    }
  }

  return (
    <div class="flex flex-col gap-4">
      <Card title="Cluster Settings">
        <Show when={overview()} fallback={<div>Loading…</div>}>
          <div class="space-y-4">
            <div class="grid md:grid-cols-3 gap-4">
              <div>
                <div class="text-xs text-neutral-500">Cluster ID</div>
                <div class="font-mono text-sm">
                  {overview()?.record?.id || clusterId()}
                </div>
              </div>
              <div>
                <div class="text-xs text-neutral-500">Name</div>
                <div>{overview()?.record?.name || '-'}</div>
              </div>
              <div>
                <div class="flex items-center justify-between">
                  <div>
                    <div class="text-xs text-neutral-500">Health</div>
                    <div>
                      {healthDetail()?.status ||
                        overview()?.record?.state ||
                        'unknown'}
                    </div>
                  </div>
                  <button
                    class="btn"
                    onClick={() => {
                      refetchOverview()
                      fetchHealthDetail()
                    }}
                  >
                    Refresh
                  </button>
                </div>
                <Show when={healthDetail()}>
                  {(h) => (
                    <div class="text-xs text-neutral-500 mt-1">
                      <Show when={h().code}>
                        <div>code: {h().code}</div>
                      </Show>
                      <Show when={h().error}>
                        <div class="text-red-600 dark:text-red-300 break-all">
                          error: {h().error}
                        </div>
                      </Show>
                    </div>
                  )}
                </Show>
              </div>
            </div>

            <div class="space-y-2">
              <label class="block text-sm">
                Rotate/Attach kubeconfig
                <textarea
                  class="mt-1 w-full h-36 rounded-md border px-3 py-2 font-mono text-xs"
                  placeholder="Paste kubeconfig YAML"
                  value={kubeconfig()}
                  onInput={(e) => setKubeconfig(e.currentTarget.value)}
                />
              </label>
              <div class="flex gap-2">
                <button
                  class="btn"
                  disabled={busy()}
                  onClick={rotateKubeconfig}
                >
                  Attach
                </button>
                <button class="btn" onClick={downloadKubeconfig}>
                  Download stored
                </button>
                <button class="btn" onClick={downloadJoinConfig}>
                  Download join config
                </button>
              </div>
            </div>

            <div class="space-y-2 pt-2 border-t">
              <div class="flex items-center justify-between">
                <div>
                  <div class="text-sm font-medium">API proxy</div>
                  <div class="text-xs text-neutral-500">
                    Set to the local kube-API served over tailnet (defaults to port 16443)
                  </div>
                </div>
                <div class="flex items-center gap-2">
                  <button class="btn" disabled={settingBusy()} onClick={setAPIProxyToTailnet}>
                    Set to tailnet endpoint
                  </button>
                  <button
                    class="btn"
                    onClick={async () => {
                      const st = await getClusterSettings(clusterId())
                      const url = st?.api_proxy_url
                      if (!url) {
                        pushToast({ type: 'error', message: 'api_proxy_url not set' })
                        return
                      }
                      const status = await clusterHealth(clusterId())
                      if (status === 'ok') {
                        pushToast({ type: 'success', message: 'Proxy OK: kube-API reachable' })
                      } else if (status === 'error') {
                        pushToast({ type: 'error', message: 'Proxy error: kube-API unreachable' })
                      } else {
                        pushToast({ type: 'info', message: 'Proxy unknown: try again shortly' })
                      }
                    }}
                  >
                    Verify via proxy
                  </button>
                </div>
              </div>
            </div>

            <div class="pt-4 border-t">
              <button
                class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-red-50 dark:bg-red-900/30 hover:bg-red-100 dark:hover:bg-red-900/50 text-red-700 dark:text-red-200"
                onClick={destroyCluster}
              >
                Delete cluster record
              </button>
            </div>
            <div class="pt-4">
              <PublishedServices clusterId={clusterId()} />
            </div>
            <div class="pt-4">
              <div class="font-medium mb-2">Federation</div>
              <MultiDevice clusterId={clusterId()} />
            </div>
          </div>
        </Show>
      </Card>
    </div>
  )
}

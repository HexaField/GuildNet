import { createEffect, createResource, createSignal, For, Show } from 'solid-js'
import { A } from '@solidjs/router'

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

function useJobs() {
  const [jobs, { refetch }] = createResource(() =>
    fetchJSON<any[]>('/api/jobs')
  )
  return { jobs, refetch }
}

export default function Deploy() {
  const { jobs, refetch } = useJobs()
  const [hsName, setHsName] = createSignal('')
  const [clName, setClName] = createSignal('')
  const [endpoint, setEndpoint] = createSignal('')
  const [preauth, setPreauth] = createSignal('')
  const [kubeconfig, setKubeconfig] = createSignal('')

  const createHeadscale = async () => {
    await fetchJSON('/api/deploy/headscale', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: hsName() || undefined })
    })
    refetch()
  }
  const createCluster = async () => {
    await fetchJSON('/api/deploy/clusters', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: clName() || undefined })
    })
    refetch()
  }

  const [headscales, { refetch: refetchHs }] = createResource(() =>
    fetchJSON<any[]>('/api/deploy/headscale')
  )
  const [clusters, { refetch: refetchCl }] = createResource(() =>
    fetchJSON<any[]>('/api/deploy/clusters')
  )

  const setHsEndpoint = async (id: string) => {
    await fetchJSON(`/api/deploy/headscale/${id}?action=endpoint`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ endpoint: endpoint() })
    })
    refetchHs()
  }
  const setHsPreauth = async (id: string) => {
    await fetchJSON(`/api/deploy/headscale/${id}?action=preauth-key`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ value: preauth() })
    })
    refetchHs()
  }
  const hsHealth = async (id: string) =>
    fetchJSON<any>(`/api/deploy/headscale/${id}?action=health`, {
      method: 'POST'
    })

  const attachKubeconfig = async (id: string) => {
    await fetchJSON(`/api/deploy/clusters/${id}?action=attach-kubeconfig`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ kubeconfig: kubeconfig() })
    })
    refetchCl()
  }
  const clHealth = async (id: string) =>
    fetchJSON<any>(`/api/deploy/clusters/${id}?action=health`, {
      method: 'POST'
    })
  const clDownloadKubeconfig = (id: string) => {
    window.open(`/api/deploy/clusters/${id}?action=kubeconfig`, '_blank')
  }

  return (
    <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
      <section class="space-y-3">
        <h2 class="text-lg font-semibold">Headscale</h2>
        <div class="flex gap-2">
          <input
            placeholder="Name"
            value={hsName()}
            onInput={(e) => setHsName(e.currentTarget.value)}
            class="border rounded px-2 py-1"
          />
          <button class="btn" onClick={createHeadscale}>
            Create
          </button>
        </div>
        <div class="flex gap-2">
          <input
            placeholder="Endpoint (e.g. https://headscale.example.com)"
            value={endpoint()}
            onInput={(e) => setEndpoint(e.currentTarget.value)}
            class="border rounded px-2 py-1 w-full"
          />
          <input
            placeholder="Preauth key"
            value={preauth()}
            onInput={(e) => setPreauth(e.currentTarget.value)}
            class="border rounded px-2 py-1 w-full"
          />
        </div>
        <div class="border rounded divide-y">
          <For each={headscales()?.slice().reverse()}>
            {(h) => (
              <div class="p-3 space-y-2">
                <div class="flex items-center justify-between">
                  <div>
                    <div class="font-medium">{h.name}</div>
                    <div class="text-xs text-neutral-500">{h.id}</div>
                  </div>
                  <div class="text-xs">{h.state}</div>
                </div>
                <div class="flex gap-2">
                  <button class="btn" onClick={() => setHsEndpoint(h.id)}>
                    Save endpoint
                  </button>
                  <button class="btn" onClick={() => setHsPreauth(h.id)}>
                    Save preauth
                  </button>
                  <button
                    class="btn"
                    onClick={async () => {
                      const x = await hsHealth(h.id)
                      alert(`Headscale health: ${x.status || 'unknown'}`)
                    }}
                  >
                    Health
                  </button>
                </div>
              </div>
            )}
          </For>
        </div>
      </section>

      <section class="space-y-3">
        <h2 class="text-lg font-semibold">Clusters</h2>
        <div class="flex gap-2">
          <input
            placeholder="Name"
            value={clName()}
            onInput={(e) => setClName(e.currentTarget.value)}
            class="border rounded px-2 py-1"
          />
          <button class="btn" onClick={createCluster}>
            Create
          </button>
        </div>
        <div class="flex gap-2">
          <textarea
            placeholder="Paste kubeconfig here"
            value={kubeconfig()}
            onInput={(e) => setKubeconfig(e.currentTarget.value)}
            class="border rounded px-2 py-1 w-full h-28"
          />
        </div>
        <div class="border rounded divide-y">
          <For each={clusters()?.slice().reverse()}>
            {(c) => (
              <div class="p-3 space-y-2">
                <div class="flex items-center justify-between">
                  <div>
                    <div class="font-medium">{c.name}</div>
                    <div class="text-xs text-neutral-500">{c.id}</div>
                  </div>
                  <div class="text-xs">{c.state}</div>
                </div>
                <div class="flex gap-2">
                  <button class="btn" onClick={() => attachKubeconfig(c.id)}>
                    Attach kubeconfig
                  </button>
                  <button
                    class="btn"
                    onClick={() => clDownloadKubeconfig(c.id)}
                  >
                    Download kubeconfig
                  </button>
                  <button
                    class="btn"
                    onClick={async () => {
                      const x = await clHealth(c.id)
                      alert(`Cluster health: ${x.status || 'unknown'}`)
                    }}
                  >
                    Health
                  </button>
                </div>
              </div>
            )}
          </For>
        </div>
      </section>

      <section class="md:col-span-2">
        <h2 class="text-lg font-semibold">Jobs</h2>
        <div class="border rounded divide-y">
          <For each={jobs()?.slice().reverse()}>
            {(j) => (
              <div class="p-3 flex items-center gap-4">
                <div class="text-xs w-40 truncate">{j.id}</div>
                <div class="w-40">{j.kind}</div>
                <div class="w-32">{j.status}</div>
                <div class="w-32">{Math.round((j.progress || 0) * 100)}%</div>
              </div>
            )}
          </For>
        </div>
      </section>
    </div>
  )
}

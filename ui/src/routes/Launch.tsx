import { createEffect, createSignal, For, Show } from 'solid-js'
import { z } from 'zod'
import Card from '../components/Card'
import Input from '../components/Input'
import {
  getImageDefaults,
  listImages,
  createClusterWorkspace,
  listSites,
  type SiteRecord
} from '../lib/api'
import { K8S_DNS_SUFFIX, K8S_NS } from '../lib/config'
import { pushToast } from '../components/Toaster'
import { useNavigate, useParams } from '@solidjs/router'

const schema = z.object({
  image: z
    .string()
    .min(1)
    .regex(/^[\w\-\.\/]+:[\w\-\.]+|^[\w\-\.\/]+$/),
  name: z.string().optional(),
  args: z.array(z.string()).optional(),
  cpu: z.string().optional(),
  memory: z.string().optional(),
  env: z.record(z.string(), z.string()).optional(),
  labels: z.record(z.string(), z.string()).optional(),
  ports: z
    .array(
      z.object({
        name: z.string().optional(),
        port: z.coerce.number().int().min(1).max(65535)
      })
    )
    .optional()
})

export default function Launch() {
  type ImageDefaults = {
    ports?: { name?: string; port: number }[]
    env?: Record<string, string>
  }
  const navigate = useNavigate()
  const params = useParams()
  const clusterId = () => params.clusterId || ''
  const [image, setImage] = createSignal('')
  const [presets, setPresets] = createSignal<
    Array<{ label: string; value: string; description?: string }>
  >([])
  const [name, setName] = createSignal('')
  const [args, setArgs] = createSignal<string[]>([])
  const [env, setEnv] = createSignal<Array<{ k: string; v: string }>>([])
  const [labels, setLabels] = createSignal<Array<{ k: string; v: string }>>([])
  const [ports, setPorts] = createSignal<Array<{ name: string; port: number }>>(
    []
  )
  const [cpu, setCpu] = createSignal('')
  const [memory, setMemory] = createSignal('')
  const [busy, setBusy] = createSignal(false)
  const [errors, setErrors] = createSignal<string[]>([])
  const [showAdvanced, setShowAdvanced] = createSignal(false)
  // Device selector
  const [sites, setSites] = createSignal<SiteRecord[]>([])
  const [targetDevice, setTargetDevice] = createSignal<string>('__current__')

  // Load presets from backend on mount
  createEffect(async () => {
    const list = await listImages().catch(() => [])
    setPresets(
      list.map((i) => ({
        label: i.label,
        value: i.image,
        description: i.description
      }))
    )
  })

  // Load sites for device selector (filter to this cluster)
  createEffect(async () => {
    const all = await listSites().catch(() => [])
    const filtered = (all || []).filter(
      (s) => (s.clusterId || '') === clusterId()
    )
    setSites(filtered)
  })

  // Prefill defaults by querying backend for the selected image
  createEffect(async () => {
    const img = image()
    if (!img) return
    let d: ImageDefaults = await getImageDefaults(img).catch(
      () => ({}) as ImageDefaults
    )
    // Client-side fallback defaults for known images (avoids requiring backend restart)
    if (
      (!d || (!d.ports && !d.env)) &&
      /(codercom|ghcr\.io\/coder)\/code-server/i.test(img)
    ) {
      d = {
        ports: [{ name: 'http', port: 8080 }],
        env: { AGENT_HOST: '' }
      } as ImageDefaults
    }
    if ((ports()?.length ?? 0) === 0 && Array.isArray(d.ports))
      setPorts(d.ports.map((p) => ({ name: p.name || '', port: p.port })))
    const existing: Record<string, string> = Object.fromEntries(
      env().map((e) => [e.k, e.v] as const)
    )
    const merged: Record<string, string> = { ...(d.env || {}), ...existing }
    const arr = Object.entries(merged).map(([k, v]) => ({ k, v }))
    setEnv(arr)
  })

  const submit = async () => {
    setErrors([])
    const envObj = Object.fromEntries(
      env()
        .filter((e) => e.k)
        .map((e) => [e.k, e.v])
    )
    const labelsObj = Object.fromEntries(
      labels()
        .filter((e) => e.k)
        .map((e) => [e.k, e.v])
    )
    // If AGENT_HOST not provided, derive from name and namespace as a Service FQDN
    const dns1123 = (s: string) =>
      s
        .toLowerCase()
        .trim()
        .replace(/[^a-z0-9-]+/g, '-')
        .replace(/^-+|-+$/g, '')
        .replace(/--+/g, '-')
    const nm = name() || ''
    const ensureAgentHost = () => {
      const current = envObj['AGENT_HOST']
      if (current && current.trim()) return current.trim()
      const base = dns1123(nm || 'workload')
      return `${base}.${K8S_NS}.${K8S_DNS_SUFFIX}`
    }
    if (!envObj['AGENT_HOST']) envObj['AGENT_HOST'] = ensureAgentHost()

    const spec = {
      image: image(),
      name: nm || undefined,
      args: args(),
      env: envObj,
      labels: labelsObj,
      resources: { cpu: cpu() || undefined, memory: memory() || undefined },
      expose: ports()
    } as any
    // Optional: schedule on a specific device
    if (targetDevice() && targetDevice() !== '__current__') {
      spec.scheduleNode = targetDevice()
    }
    const parsed = schema.safeParse({
      image: spec.image,
      name: spec.name,
      args: spec.args,
      env: spec.env,
      labels: spec.labels,
      cpu: spec.resources.cpu,
      memory: spec.resources.memory,
      ports: spec.expose
    })
    if (!parsed.success) {
      setErrors(
        parsed.error.issues.map(
          (e) => `${(e.path ?? []).join('.')}: ${e.message}`
        )
      )
      pushToast({ type: 'error', message: 'Validation failed' })
      return
    }
    setBusy(true)
    try {
      const res = await createClusterWorkspace(clusterId(), {
        image: spec.image,
        name: spec.name,
        env: Object.entries(spec.env || {}).map(([name, value]) => ({
          name,
          value
        })),
        ports: (spec.expose || []).map((p: any) => ({
          name: p.name,
          containerPort: p.port
        })),
        args: spec.args,
        resources: spec.resources,
        labels: Object.entries(spec.labels || {}).map(([name, value]) => ({
          name,
          value
        })),
        // Pass scheduleNode when a target device is selected; backend will honor it
        ...(spec.scheduleNode ? { scheduleNode: spec.scheduleNode } : {})
      })
      if (res) {
        pushToast({ type: 'success', message: `Workspace ${res.id} creating` })
        navigate(
          `/c/${encodeURIComponent(clusterId())}/servers/${encodeURIComponent(res.id)}`
        )
      }
    } catch (e) {
      pushToast({ type: 'error', message: (e as Error).message })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div class="flex flex-col gap-4">
      <Card title="Launch new workload">
        <div class="grid md:grid-cols-2 gap-4">
          <div class="space-y-3">
            <label class="block text-sm">
              Image
              <div class="mt-1 grid grid-cols-1 gap-2">
                <select
                  class="w-full rounded-md border px-3 py-2 bg-white dark:bg-neutral-900"
                  value={image()}
                  onChange={async (e) => {
                    const v = e.currentTarget.value
                    setImage(v)
                  }}
                >
                  <option value="">Select an image…</option>
                  <For each={presets()}>
                    {(p) => <option value={p.value}>{p.label}</option>}
                  </For>
                </select>
                <Input
                  value={image()}
                  onInput={(e) => setImage(e.currentTarget.value)}
                  placeholder="…or enter a custom image (e.g., ghcr.io/org/app:tag)"
                />
              </div>
            </label>
            <label class="block text-sm">
              Target device
              <select
                class="mt-1 w-full rounded-md border px-3 py-2 bg-white dark:bg-neutral-900"
                value={targetDevice()}
                onChange={(e) => setTargetDevice(e.currentTarget.value)}
              >
                <option value="__current__">This device (default)</option>
                <For each={sites()}>
                  {(s) => (
                    <option value={s.name || s.id}>
                      {(s.name || s.id) +
                        (Array.isArray(s.tailnetIPs) && s.tailnetIPs.length > 0
                          ? ` — ${s.tailnetIPs.join(',')}`
                          : '')}
                    </option>
                  )}
                </For>
              </select>
              <div class="text-xs text-neutral-500 mt-1">
                If unset, the workload launches on this device. Selecting a
                device sets a scheduling hint so Kubernetes places the pod on
                that node.
              </div>
            </label>
            <label class="block text-sm">
              Name
              <Input
                value={name()}
                onInput={(e) => setName(e.currentTarget.value)}
                placeholder="optional"
              />
            </label>
            <label class="block text-sm">
              Args
              <div class="space-y-2">
                {args().map((a, i) => (
                  <div class="flex gap-2">
                    <Input
                      value={a}
                      onInput={(e) =>
                        setArgs(args().with(i, e.currentTarget.value))
                      }
                    />
                    <button
                      class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                      onClick={() =>
                        setArgs(args().filter((_, idx) => idx !== i))
                      }
                    >
                      Remove
                    </button>
                  </div>
                ))}
                <button class="btn" onClick={() => setArgs([...args(), ''])}>
                  Add arg
                </button>
              </div>
            </label>
            <div>
              <button
                class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                onClick={() => setShowAdvanced(!showAdvanced())}
              >
                {showAdvanced() ? 'Hide advanced' : 'Show advanced'}
              </button>
            </div>
            <Show when={showAdvanced()}>
              <div class="grid grid-cols-2 gap-2">
                <label class="block text-sm">
                  CPU
                  <Input
                    value={cpu()}
                    onInput={(e) => setCpu(e.currentTarget.value)}
                    placeholder="500m"
                  />
                </label>
                <label class="block text-sm">
                  Memory
                  <Input
                    value={memory()}
                    onInput={(e) => setMemory(e.currentTarget.value)}
                    placeholder="256Mi"
                  />
                </label>
              </div>
            </Show>
          </div>
          <div class="space-y-3">
            <Show when={showAdvanced()}>
              <label class="block text-sm">
                Env
                <div class="space-y-2">
                  {env().map((p, i) => (
                    <div class="flex gap-2">
                      <Input
                        placeholder="KEY"
                        value={p.k}
                        onInput={(e) =>
                          setEnv(
                            env().with(i, { ...p, k: e.currentTarget.value })
                          )
                        }
                      />
                      <Input
                        placeholder="value"
                        value={p.v}
                        onInput={(e) =>
                          setEnv(
                            env().with(i, { ...p, v: e.currentTarget.value })
                          )
                        }
                      />
                      <button
                        class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                        onClick={() =>
                          setEnv(env().filter((_, idx) => idx !== i))
                        }
                      >
                        Remove
                      </button>
                    </div>
                  ))}
                  <button
                    class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                    onClick={() => setEnv([...env(), { k: '', v: '' }])}
                  >
                    Add env
                  </button>
                  <div class="text-xs text-neutral-500">
                    Tip: Image defaults prefill suggested env and ports; you can
                    override as needed. For IDE, AGENT_HOST should be the
                    agent's reachable DNS or IP (e.g., service name).
                  </div>
                </div>
              </label>
              <label class="block text-sm">
                Labels
                <div class="space-y-2">
                  {labels().map((p, i) => (
                    <div class="flex gap-2">
                      <Input
                        placeholder="key"
                        value={p.k}
                        onInput={(e) =>
                          setLabels(
                            labels().with(i, { ...p, k: e.currentTarget.value })
                          )
                        }
                      />
                      <Input
                        placeholder="value"
                        value={p.v}
                        onInput={(e) =>
                          setLabels(
                            labels().with(i, { ...p, v: e.currentTarget.value })
                          )
                        }
                      />
                      <button
                        class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                        onClick={() =>
                          setLabels(labels().filter((_, idx) => idx !== i))
                        }
                      >
                        Remove
                      </button>
                    </div>
                  ))}
                  <button
                    class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                    onClick={() => setLabels([...labels(), { k: '', v: '' }])}
                  >
                    Add label
                  </button>
                </div>
              </label>
              <label class="block text-sm">
                Ports
                <div class="space-y-2">
                  {ports().map((p, i) => (
                    <div class="flex gap-2">
                      <Input
                        placeholder="name"
                        value={p.name}
                        onInput={(e) =>
                          setPorts(
                            ports().with(i, {
                              ...p,
                              name: e.currentTarget.value
                            })
                          )
                        }
                      />
                      <Input
                        placeholder="port"
                        type="number"
                        value={String(p.port)}
                        onInput={(e) =>
                          setPorts(
                            ports().with(i, {
                              ...p,
                              port: Number(e.currentTarget.value)
                            })
                          )
                        }
                      />
                      <button
                        class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                        onClick={() =>
                          setPorts(ports().filter((_, idx) => idx !== i))
                        }
                      >
                        Remove
                      </button>
                    </div>
                  ))}
                  <button
                    class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
                    onClick={() =>
                      setPorts([...ports(), { name: '', port: 0 }])
                    }
                  >
                    Add port
                  </button>
                </div>
              </label>
            </Show>
          </div>
        </div>
        {errors().length > 0 && (
          <div class="text-sm text-red-600 mt-2">
            {errors().map((e) => (
              <div>{e}</div>
            ))}
          </div>
        )}
        <div class="mt-4">
          <button
            class="inline-flex items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium border bg-neutral-50 dark:bg-neutral-800 hover:bg-neutral-100 dark:hover:bg-neutral-700"
            disabled={busy()}
            onClick={submit}
          >
            {busy() ? 'Submitting…' : 'Launch'}
          </button>
        </div>
      </Card>
    </div>
  )
}

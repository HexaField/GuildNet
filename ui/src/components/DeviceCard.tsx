import { Show } from 'solid-js'
import { timeAgo } from '../lib/format'
import type { Site } from '../lib/schemas'

export type DeviceCardProps = {
  device: Partial<Site> & Record<string, any>
}

export default function DeviceCard(props: DeviceCardProps) {
  const s = props.device as any
  const name = s.name || s.id
  const tailnet = Array.isArray(s.tailnetIPs) ? s.tailnetIPs : []

  const parseLastSeen = (v: any): number | null => {
    try {
      if (v == null) return null
      if (typeof v === 'string') {
        const n = Date.parse(v)
        if (!Number.isNaN(n)) return n
        const pn = parseInt(v, 10)
        if (!Number.isNaN(pn)) return pn > 1e12 ? pn : pn * 1000
        return null
      }
      if (typeof v === 'number') {
        return v > 1e12 ? v : v * 1000
      }
      if (typeof v === 'object') {
        if (typeof v.seconds === 'number') {
          const sec = v.seconds
          const ns = typeof v.nanos === 'number' ? v.nanos : 0
          return sec * 1000 + Math.floor(ns / 1e6)
        }
        if (v.Time) {
          const n = Date.parse(String(v.Time))
          if (!Number.isNaN(n)) return n
        }
      }
      return null
    } catch {
      return null
    }
  }

  const lastTs = parseLastSeen(s.lastSeen as any)
  const cpuMilli = typeof s.cpuMilli === 'number' ? s.cpuMilli : undefined
  const memoryMB = typeof s.memoryMB === 'number' ? s.memoryMB : undefined
  const storageMB = typeof s.storageMB === 'number' ? s.storageMB : undefined
  const vramMB = typeof s.vramMB === 'number' ? s.vramMB : undefined
  const supportsCluster = !!s.supportsCluster
  const online =
    (lastTs != null ? Date.now() - lastTs < 5 * 60 * 1000 : false) ||
    String(s.state).toLowerCase() === 'online'

  const fmtCPU = (m?: number) =>
    m == null ? '' : `${(m / 1000).toFixed(1)} CPU`
  const fmtMB = (mb?: number) => {
    if (mb == null) return ''
    if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GiB`
    return `${mb} MiB`
  }

  return (
    <div class="py-2 border-b last:border-b-0">
      <div class="flex items-center justify-between">
        <div class="text-sm font-medium">{name}</div>
        <Show
          when={s.self === true}
          fallback={
            <div
              class={`text-[10px] px-2 py-0.5 rounded-full border ${online ? 'bg-green-50 text-green-700 border-green-200' : 'bg-neutral-50 text-neutral-600 border-neutral-200'}`}
            >
              {online ? 'online' : 'offline'}
            </div>
          }
        >
          <span class="text-[10px] px-2 py-0.5 rounded-full border border-neutral-300 text-neutral-300">
            This device
          </span>
        </Show>
      </div>
      <div class="text-xs text-neutral-500 break-words">{s.id}</div>
      <Show when={tailnet.length > 0}>
        <div class="text-xs text-neutral-500 mt-0.5">
          IPs: {tailnet.join(', ')}
        </div>
      </Show>
      <div class="text-xs text-neutral-500 mt-0.5 flex flex-wrap gap-x-2 gap-y-1">
        <Show when={cpuMilli != null}>
          <span>{fmtCPU(cpuMilli)}</span>
        </Show>
        <Show when={memoryMB != null}>
          <span>RAM {fmtMB(memoryMB)}</span>
        </Show>
        <Show when={storageMB != null}>
          <span>Disk {fmtMB(storageMB)}</span>
        </Show>
        <Show when={vramMB != null}>
          <span>VRAM {fmtMB(vramMB)}</span>
        </Show>
      </div>
      <div class="text-[11px] text-neutral-400 mt-0.5">
        <Show when={!s.self}>
          <Show when={lastTs != null} fallback={<span>last seen: —</span>}>
            last seen: {timeAgo(new Date(lastTs!).toISOString())}
          </Show>
        </Show>
        <Show when={supportsCluster}>
          <span class="ml-2 px-1.5 py-0.5 rounded bg-blue-50 text-blue-700 border border-blue-200 text-[10px] align-middle">
            supports cluster
          </span>
        </Show>
      </div>
    </div>
  )
}

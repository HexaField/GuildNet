export type Port = { port: number; name?: string }

export type Server = {
  id: string
  name: string
  image: string
  status: 'pending' | 'running' | 'failed' | 'stopped'
  node?: string
  created_at?: string
  updated_at?: string
  url?: string
  ports?: Port[]
  resources?: { cpu?: string; memory?: string }
  args?: string[]
  env?: Record<string, string>
  events?: Array<{
    t?: string
    type?: string
    message?: string
    status?: string
  }>
}

export type LogLine = {
  t?: string
  lvl?: 'info' | 'debug' | 'error' | string
  msg?: string
}

export type ServerEvent = {
  type: string // e.g., server.updated
  id?: string
  status?: Server['status']
  [k: string]: unknown
}

export type JobSpec = {
  name?: string
  image: string
  args?: string[]
  env?: Record<string, string>
  resources?: { cpu?: string; memory?: string }
  labels?: Record<string, string>
  expose?: Port[]
}

export type JobAccepted = { id: string; status: 'pending' | string }

export type DeployImage = {
  label: string
  image: string
  description?: string
}

export type SiteRecord = {
  id: string
  name?: string
  state?: string
  tailnetIPs?: string[]
  supportsCluster?: boolean
  cpuMilli?: number
  memoryMB?: number
  storageMB?: number
  vramMB?: number
  lastSeen?: string
}

export type MDSummary = {
  clusterId: string
  namespace: string
  name: string
}

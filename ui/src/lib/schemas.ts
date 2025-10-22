import { z } from 'zod'

export const SiteSchema = z.object({
  clusterId: z.string().optional(),
  cpuMilli: z.number().int().optional(),
  createdAt: z.string().optional(),
  forwards: z.number().int().optional(),
  hasDB: z.boolean().optional(),
  hasK8s: z.boolean().optional(),
  id: z.string(),
  lastSeen: z.string().optional(),
  memoryMB: z.number().int().optional(),
  name: z.string().optional(),
  started: z.boolean().optional(),
  state: z.union([z.string(), z.boolean()]).optional(),
  stateDir: z.string().optional(),
  tailnetIPs: z.array(z.string()).optional(),
  supportsCluster: z.union([z.boolean(), z.null()]).optional(),
  storageMB: z.number().int().optional(),
  vramMB: z.number().int().optional()
})

export const SiteListSchema = z.array(SiteSchema)

export const MDSummarySchema = z.object({
  id: z.string(),
  namespace: z.string(),
  name: z.string()
})

export const MDSummaryListSchema = z.array(MDSummarySchema)

// Generic per-site status: allow unknown shape for now
export const PerSiteStatusSchema = z.unknown()

// Minimal validation schema for creating/updating a FederatedService
export const FederatedServiceInputSchema = z.object({
  apiVersion: z.string().optional(),
  kind: z.string().optional(),
  metadata: z
    .object({
      name: z.string().optional(),
      namespace: z.string().optional(),
      labels: z.record(z.string(), z.string()).optional()
    })
    .optional(),
  spec: z.record(z.string(), z.any()).optional()
})

export type Site = z.infer<typeof SiteSchema>
export type MDSummary = z.infer<typeof MDSummarySchema>
export type FederatedServiceInput = z.infer<typeof FederatedServiceInputSchema>

import { z } from 'zod'

export const SiteSchema = z.object({
  id: z.string(),
  name: z.string().optional(),
  state: z.string().optional()
})

export const SiteListSchema = z.array(SiteSchema)

export const MDSummarySchema = z.object({
  clusterId: z.string(),
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
  metadata: z.object({
    name: z.string().optional(),
    namespace: z.string().optional(),
    labels: z.record(z.string(), z.string()).optional()
  }).optional(),
  spec: z.record(z.string(), z.any()).optional()
})

export type Site = z.infer<typeof SiteSchema>
export type MDSummary = z.infer<typeof MDSummarySchema>
export type FederatedServiceInput = z.infer<typeof FederatedServiceInputSchema>

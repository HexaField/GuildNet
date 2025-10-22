import { createSignal } from 'solid-js'
import { useNavigate, useParams } from '@solidjs/router'
import {
  FederatedServiceInputSchema,
  FederatedServiceInput
} from '../lib/schemas'
import { createFederatedService } from '../lib/api'

export default function FederatedCreate() {
  const navigate = useNavigate()
  const params = useParams()
  const clusterId = () => params.clusterId || 'default'
  const [name, setName] = createSignal('')
  const [namespace, setNamespace] = createSignal('default')
  const [error, setError] = createSignal<string | null>(null)
  const [loading, setLoading] = createSignal(false)

  const submit = async (e: Event) => {
    e.preventDefault()
    setError(null)
    const input: FederatedServiceInput = {
      metadata: { name: name(), namespace: namespace() },
      spec: {}
    } as any
    const parsed = FederatedServiceInputSchema.safeParse(input)
    if (!parsed.success) {
      setError(JSON.stringify(parsed.error.format(), null, 2))
      return
    }
    try {
      setLoading(true)
      await createFederatedService(clusterId(), parsed.data as any)
      navigate(`/c/${encodeURIComponent(clusterId())}/federation`)
    } catch (err: any) {
      setError(err?.message || String(err))
    } finally {
      setLoading(false)
    }
  }

  return (
    <div>
      <h2>Create Federated service</h2>
      <form onSubmit={submit}>
        <label>
          Name
          <input
            value={name()}
            onInput={(e) => setName((e.target as HTMLInputElement).value)}
          />
        </label>
        <label>
          Namespace
          <input
            value={namespace()}
            onInput={(e) => setNamespace((e.target as HTMLInputElement).value)}
          />
        </label>
        <div>
          <button type="submit" disabled={loading()}>
            Create
          </button>
        </div>
        {error() && <pre style={{ color: 'red' }}>{error()}</pre>}
      </form>
    </div>
  )
}

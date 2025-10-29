Multi-device-only mode

This repository aims to support multi-device (federated) cluster deployments only.

To enforce multi-device-only operation at runtime, set the environment variable:

  GUILDNET_MULTI_DEVICE_ONLY=true

Effect:
- The internal `k8s.New()` helper will refuse to create an implicit in-cluster or local kubeconfig-based client.
- All actuation must go through the per-cluster Registry and `k8s.NewFromKubeconfig` with explicit kubeconfigs.

Operational notes:
- If you run a single-cluster operator for development, temporarily unset the environment variable.
- CI and production should run with precise per-cluster kubeconfigs managed by the registry and site join flow.

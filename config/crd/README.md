This directory contains Kubernetes CustomResourceDefinition (CRD) manifests for GuildNet.

Regeneration (run locally):

1. Install controller-gen (locally use a recent stable version) or use the Makefile helper:

   ```sh
   # preferred: install into your GOPATH/bin
   go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.12.0

   # or use the repository Makefile which will try to install controller-gen if missing
   make gen
   ```

2. Alternative: run the generator in a pinned container if your local environment is unstable:

   ```sh
   ./scripts/gen-in-container.sh
   ```

Notes and fallback:
- In some development environments controller-gen may crash during Go type-checking (nil-pointer panic). If that happens, prefer using `./scripts/gen-in-container.sh` to run the generator in a clean container.
- This repo contains minimal hand-authored CRD stubs under `config/crd/bases/` to allow local experimentation without generated artifacts. These stubs are intentionally minimal; prefer regenerating CRDs with controller-gen and committing results.

If you modify API types under `api/v1alpha1`, regenerate CRDs and generated deepcopy code and commit them to keep the repo consistent.

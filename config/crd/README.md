This directory contains Kubernetes CustomResourceDefinition (CRD) manifests for GuildNet.

Regeneration (recommended in CI):

1. Install controller-gen (locally use a recent stable version):

   ```sh
   go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.12.0
   ```

2. Run the generator from the repository root:

   ```sh
   controller-gen object paths=./api/... crd:crdVersions=v1 output:crd:dir=config/crd
   ```

Notes and fallback:
- In some development environments controller-gen may crash during Go type-checking (nil-pointer panic). If that happens, run the generator in a clean CI environment (this repository supplies a GitHub Actions job `.github/workflows/gen-and-test.yml` which installs controller-gen and runs `make gen`).
- This repo contains minimal hand-authored CRD stubs under `config/crd/bases/` to allow local experimentation without generated artifacts. These stubs are intentionally minimal; prefer regenerating CRDs with controller-gen in CI and committing results.

If you modify API types under `api/v1alpha1`, regenerate CRDs and generated deepcopy code and commit them to keep the repo consistent.

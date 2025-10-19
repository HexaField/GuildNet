This directory contains Kubernetes CustomResourceDefinition (CRD) manifests for GuildNet.

Regeneration (run locally):

1. Install controller-gen (locally use a recent stable version) or use the Makefile helper:

   ```sh
   # install into your GOPATH/bin
   go install sigs.k8s.io/controller-tools/cmd/controller-gen@v0.15.0

   # or use the repository Makefile which will try to install controller-gen if missing
   make gen
   ```

Notes:
- Ensure `$(go env GOPATH)/bin` is on your PATH so `controller-gen` can be found by the Makefile.
- This repo contains minimal hand-authored CRD stubs under `config/crd/bases/` to allow local experimentation without generated artifacts. These stubs are intentionally minimal; prefer regenerating CRDs with controller-gen and committing results.

If you modify API types under `api/v1alpha1`, regenerate CRDs and generated deepcopy code and commit them to keep the repo consistent.

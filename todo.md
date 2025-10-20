- [ ] consolidate LOCAL_LISTEN and VITE_API_BASE to a single var
- [ ] more clean up

Completed recent tasks:

- [x] Apply CRDs and verify Established status
- [x] Run operator with control-plane kubeconfig (GN_CONTROL_PLANE_KUBECONFIG)
- [x] Import operator image locally (no registry) and set imagePullPolicy
- [x] Smoke test: create a Workspace and verify operator reconciliation
- [x] Fix sample workspace CrashLoopBackOff by preferring non-root nginx image

Next actions:

- [ ] Cleanup sample Workspace and generated resources (Deployment/Service/ReplicaSet/Pods)
- [ ] Make `WORKSPACE_NGINX_UNPRIVILEGED_IMAGE` configurable (done) and add tests/docs
- [ ] Add Option B runbook to `DEPLOYMENT.md` (done)
- [ ] Update `API.md` with GN_CONTROL_PLANE_KUBECONFIG and operator-image notes (done)

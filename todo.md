- [ ] consolidate LOCAL_LISTEN and VITE_API_BASE to a single var
- [ ] more clean up
- [x] Prevent unintended HostApp shutdowns: ignore SIGHUP/QUIT and document pdeathsig behavior in docs

# Recent
- [x] Strict multi-device verifier: require >=2 nodes on different devices; enforce remote perspective and placement; updated DEPLOYMENT.md and architecture.md
- [x] Remote worker helper: added mounts for /opt/cni/bin and /etc/cni/net.d, pre-create xtables lock; documented multi-device join flow in DEPLOYMENT.md
- [x] Fixed per-cluster settings route docs to use /api/settings/cluster/{id}; added optional REMOTE_API_PROXY_URL handling in verifier to configure APIProxyURL on remote
- [x] UI: Added "Deploy new local cluster" option to Connect modal and wired Deployment Manager route (/deploy) for Headscale/Tailscale and cluster management; updated API/architecture/deployment docs

# New (2025-11-01)
- [x] UI (/deploy): Inline job status per row using lastJobId; add quick Tail logs per row
- [x] UI (/deploy): Multi-console live job logs (open multiple WS streams, close independently)
- [x] Settings: Add "Verify kube-API via proxy" button that uses POST /api/deploy/clusters/{id}?action=health with configured api_proxy_url
- [x] Docs: Updated API.md (WS notes, lastJobId inline status, verify via proxy), architecture.md (multi-console + inline status), DEPLOYMENT.md (multi-console + verify via proxy)

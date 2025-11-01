- [ ] consolidate LOCAL_LISTEN and VITE_API_BASE to a single var
- [ ] more clean up
- [x] Prevent unintended HostApp shutdowns: ignore SIGHUP/QUIT and document pdeathsig behavior in docs

# Recent
- [x] Strict multi-device verifier: require >=2 nodes on different devices; enforce remote perspective and placement; updated DEPLOYMENT.md and architecture.md
- [x] Remote worker helper: added mounts for /opt/cni/bin and /etc/cni/net.d, pre-create xtables lock; documented multi-device join flow in DEPLOYMENT.md
- [x] Fixed per-cluster settings route docs to use /api/settings/cluster/{id}; added optional REMOTE_API_PROXY_URL handling in verifier to configure APIProxyURL on remote

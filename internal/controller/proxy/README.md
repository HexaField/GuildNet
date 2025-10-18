Proxy controller scaffold
------------------------

This folder contains a minimal scaffold for a per-host proxy controller.

- `controller.go` implements a basic Reconciler that writes a per-service
  ConfigMap with endpoints and ensures a placeholder DaemonSet exists. The
  implementation is intentionally minimal and intended to be extended.

Notes:
- The controller is a scaffold for early integration tests and local development.
- The real proxy image, configuration format and endpoint computation must be
  implemented and hardened before production use.

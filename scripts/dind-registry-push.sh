#!/usr/bin/env bash
echo "Deprecated: DinD support has been removed. Push from your local Docker to a registry directly, or import into MicroK8s via 'microk8s ctr -n k8s.io images import'. See DEPLOYMENT.md." >&2
exit 1

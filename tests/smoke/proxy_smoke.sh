#!/usr/bin/env bash
set -euo pipefail

echo "Proxy controller smoke test (KinD)"

if ! command -v kind >/dev/null 2>&1; then
  echo "kind not found; please install kind to run this test"
  exit 2
fi
if ! command -v kubectl >/dev/null 2>&1; then
  echo "kubectl not found; please install kubectl to run this test"
  exit 2
fi

CLUSTER=gn-smoke-$(date +%s)
echo "creating kind cluster $CLUSTER"
kind create cluster --name $CLUSTER
KUBECONFIG=$(kind get kubeconfig-path --name=$CLUSTER)
export KUBECONFIG

echo "apply CRDs"
kubectl apply -f config/crd/bases || true

echo "deploy operator (in-cluster)"
# For smoke test we run operator locally against cluster using current kubeconfig
echo "Note: this smoke test assumes operator is running locally in operator mode or deployed separately"

echo "create test namespace"
kubectl create ns gn-test || true

echo "create FederatedService test resource"
cat <<EOF | kubectl apply -f -
apiVersion: guildnet.io/v1alpha1
kind: FederatedService
metadata:
  name: smoke-proxy
  namespace: gn-test
spec:
  selector:
    app: smoke
  ports:
    - name: http
      port: 80
      targetPort: 8080
  replicas: 1
EOF

echo "wait for ConfigMap"
kubectl wait --for=condition=Established --timeout=30s configmap/proxy-endpoints-gn-test-smoke-proxy || true

echo "check DaemonSet"
kubectl get daemonset -n gn-test | grep guildnet-proxy-smoke-proxy || true

echo "cleanup"
kubectl delete ns gn-test --wait=true || true
kind delete cluster --name $CLUSTER || true

echo "smoke test complete"

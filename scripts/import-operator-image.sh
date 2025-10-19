#!/usr/bin/env bash
set -euo pipefail
# Usage: import-operator-image.sh <image> [--remote user@host] [--deploy ns/deployment[:container]] [--policy IfNotPresent|Never]

IMAGE=${1:-}
if [ -z "${IMAGE}" ]; then
  echo "Usage: $0 <image> [--remote user@host] [--deploy ns/deployment[:container]] [--policy IfNotPresent|Never]"
  exit 2
fi

REMOTE=""
DEPLOY=""
POLICY="IfNotPresent"
shift || true
while [ "$#" -gt 0 ]; do
  case "$1" in
    --remote)
      REMOTE="$2"; shift 2;;
    --deploy)
      DEPLOY="$2"; shift 2;;
    --policy)
      POLICY="$2"; shift 2;;
    *) echo "Unknown arg: $1"; exit 2;;
  esac
done

TAR=/tmp/$(echo "$IMAGE" | tr '/:@' '-')-img.tar

echo "Saving image $IMAGE to $TAR..."
docker save -o "$TAR" "$IMAGE"

if [ -n "$REMOTE" ]; then
  echo "Copying $TAR to $REMOTE:/tmp/"
  scp "$TAR" "$REMOTE:/tmp/" || { echo "scp failed"; exit 1; }
  echo "Importing image on remote host $REMOTE..."
  ssh "$REMOTE" bash -lc "sudo microk8s ctr images import /tmp/$(basename $TAR) && sudo microk8s ctr images ls | grep -F '$(echo $IMAGE | cut -d':' -f1)'" || { echo "remote import failed"; exit 1; }
  if [ -n "$DEPLOY" ]; then
    NS=$(echo "$DEPLOY" | cut -d'/' -f1)
    REST=$(echo "$DEPLOY" | cut -d'/' -f2)
    DEPLOY_NAME=$(echo "$REST" | cut -d':' -f1)
    CONTAINER_NAME=$(echo "$REST" | grep ':' || true | cut -d':' -f2)
    if [ -z "$CONTAINER_NAME" ]; then
      # default to 'operator' or 'workspace-operator' heuristics
      CONTAINER_NAME=operator
    fi
    echo "Patching deployment $DEPLOY_NAME in ns $NS to use image $IMAGE and imagePullPolicy=$POLICY"
    ssh "$REMOTE" bash -lc "sudo microk8s kubectl -n $NS set image deployment/$DEPLOY_NAME $CONTAINER_NAME=$IMAGE || true; sudo microk8s kubectl -n $NS patch deployment $DEPLOY_NAME -p '\"{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"$CONTAINER_NAME\",\"imagePullPolicy\":\"$POLICY\"}]}}}}\'' || true; sudo microk8s kubectl -n $NS rollout restart deployment/$DEPLOY_NAME || true"
  else
    echo "No deployment patch requested. Remote import complete."
  fi
else
  echo "Importing image locally into microk8s..."
  sudo microk8s ctr images import "$TAR"
  echo "Imported. To patch a deployment, run:"
  echo "  microk8s kubectl -n <ns> set image deployment/<name> <container>=${IMAGE}"
  if [ -n "$DEPLOY" ]; then
    NS=$(echo "$DEPLOY" | cut -d'/' -f1)
    REST=$(echo "$DEPLOY" | cut -d'/' -f2)
    DEPLOY_NAME=$(echo "$REST" | cut -d':' -f1)
    CONTAINER_NAME=$(echo "$REST" | grep ':' || true | cut -d':' -f2)
    if [ -z "$CONTAINER_NAME" ]; then
      CONTAINER_NAME=operator
    fi
    echo "Applying deployment patch to $NS/$DEPLOY_NAME (imagePullPolicy=$POLICY)"
    sudo microk8s kubectl -n "$NS" set image deployment/$DEPLOY_NAME $CONTAINER_NAME="$IMAGE" || true
    sudo microk8s kubectl -n "$NS" patch deployment $DEPLOY_NAME -p '{"spec":{"template":{"spec":{"containers":[{"name":"'$CONTAINER_NAME'","imagePullPolicy":"'$POLICY'"}]}}}}' || true
    sudo microk8s kubectl -n "$NS" rollout restart deployment/$DEPLOY_NAME || true
  fi
fi

echo "Cleaning up local tar: $TAR"
rm -f "$TAR"
echo "Done."

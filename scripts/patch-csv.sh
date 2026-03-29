#!/usr/bin/env bash

MIN_KUBE_VERSION="1.22.1"
NAMESPACE="spark-operator"
FILE="operator/bundle/manifests/operator.clusterserviceversion.yaml"

# Check if file exists
if [[ ! -f "$FILE" ]]; then
    echo "Error: $FILE not found."
    exit 1
fi

echo "Starting patching operations on $FILE..."

# 1. Set minKubeVersion
yq eval ".spec.minKubeVersion = \"$MIN_KUBE_VERSION\"" -i "$FILE"
echo "Set minKubeVersion to $MIN_KUBE_VERSION"

# 2. Set installModes: OwnNamespace to true, others to false
# This targets the array of objects in .spec.installModes
yq eval '.spec.installModes |= map(select(.type == "OwnNamespace").supported = true)' -i "$FILE"
yq eval '.spec.installModes |= map(select(.type != "OwnNamespace").supported = false)' -i "$FILE"
echo "Updated installModes: OwnNamespace set to true, all others set to false"

# 3. Set namespace
yq eval ".metadata.namespace = \"$NAMESPACE\"" -i "$FILE"
echo "Set metadata.namespace to $NAMESPACE"

echo "Patching complete."

#!/usr/bin/env bash

set -e

NAMESPACE="spark-operator"

usage() {
  cat <<EOF
Usage: $0 -f <file> -m <minKubeVersion> [-o <oldOperatorVersion>]

Options:
  -f, --file                    File to patch
  -m, --min-kube-version        Minimum kubernetes value
  -o, --old-operator-version    Expected old minKubeVersion (optional safety check)
  -h, --help                    Show this help message
EOF
  exit 1
}

OPTS=$(getopt -o f:m:o:h \
  --long file:,min-kube-version:,old-operator-version:,help \
  -n 'patch-csv.sh' -- "$@")

if [[ $? -ne 0 ]]; then
  usage
fi

eval set -- "$OPTS"

while true; do
  case "$1" in
    -f|--file)
      FILE="$2"
      shift 2
      ;;
    -m|--min-kube-version)
      MIN_KUBE_VERSION="$2"
      shift 2
      ;;
    -o|--old-operator-version)
      OLD_OPERATOR_VERSION="$2"
      shift 2
      ;;
    -h|--help)
      usage
      ;;
    --)
      shift
      break
      ;;
    *)
      usage
      ;;
  esac
done

# Validate required arguments
if [[ -z "$FILE" || -z "$MIN_KUBE_VERSION" ]]; then
  echo "Error: --file and --min-kube-version are required."
  usage
fi

# Check file existence
if [[ ! -f "$FILE" ]]; then
    echo "Error: $FILE not found."
    exit 1
fi

echo "Starting patching operations on $FILE..."

# 1. Set minKubeVersion
yq eval ".spec.minKubeVersion = \"$MIN_KUBE_VERSION\"" -i "$FILE"
echo "Set minKubeVersion to $MIN_KUBE_VERSION"

# # 2. Set installModes: OwnNamespace to true, others to false
# # This targets the array of objects in .spec.installModes
# yq eval '.spec.installModes |= map(select(.type == "OwnNamespace").supported = true)' -i "$FILE"
# yq eval '.spec.installModes |= map(select(.type != "OwnNamespace").supported = false)' -i "$FILE"
# echo "Updated installModes: OwnNamespace set to true, all others set to false"

# 3. Set namespace
yq eval ".metadata.namespace = \"$NAMESPACE\"" -i "$FILE"
echo "Set metadata.namespace to $NAMESPACE"


# 4. Set spec.replaces if OLD_OPERATOR_VERSION is provided
if [[ -n "$OLD_OPERATOR_VERSION" ]]; then
  yq eval ".spec.replaces = \"spark-operator.v${OLD_OPERATOR_VERSION}\"" -i "$FILE"
  echo "Set spec.replaces to spark-operator.v${OLD_OPERATOR_VERSION}"
fi

echo "Patching complete."

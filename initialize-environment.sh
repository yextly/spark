#!/usr/bin/env bash

if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi

VERSION="${1#v}"
export VERSION="$1"

export USERNAME=yextly
export PROJECTNAME=spark

# location where the operator image is hosted
export IMG=docker.io/$USERNAME/$PROJECTNAME-operator:v$VERSION

# location where the bundle will be hosted
export BUNDLE_IMG=docker.io/$USERNAME/$PROJECTNAME-operator-bundle:v$VERSION


echo "VERSION=$VERSION"
echo "IMG=$IMG"
echo "BUNDLE_IMG=$BUNDLE_IMG"

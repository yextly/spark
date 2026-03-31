#!/usr/bin/env bash

if [ -z "$1" ]; then
  echo "Usage: $0 <version>"
  exit 1
fi

INTERNALVERSION=$1
export VERSION="${INTERNALVERSION#v}"

export USERNAME=yextly
export PROJECTNAME=spark

# location where the operator image is hosted
export IMG=docker.io/$USERNAME/$PROJECTNAME-operator:$VERSION

# location where the bundle will be hosted
export BUNDLE_IMG=docker.io/$USERNAME/$PROJECTNAME-operator-bundle:$VERSION

echo "VERSION=$VERSION"
echo "IMG=$IMG"
echo "BUNDLE_IMG=$BUNDLE_IMG"

if [ -z "${GITHUB_ENV}" ]; then
  echo "Suppressed output for github"
else
  echo "VERSION=$VERSION" >> "$GITHUB_ENV"
  echo "USERNAME=$USERNAME" >> "$GITHUB_ENV"
  echo "PROJECTNAME=$PROJECTNAME" >> "$GITHUB_ENV"
  echo "IMG=$IMG" >> "$GITHUB_ENV"
  echo "BUNDLE_IMG=$BUNDLE_IMG" >> "$GITHUB_ENV"
fi

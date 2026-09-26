# Spark Operator

[![Release Operator](https://github.com/yextly/spark/actions/workflows/release.yaml/badge.svg)](https://github.com/yextly/spark/actions/workflows/release.yaml) ![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/yextly/spark?sort=semver) [![License](https://img.shields.io/github/license/yextly/spark)](LICENSE) ![Dependabot](https://img.shields.io/badge/Dependabot-enabled-brightgreen)

---

The **Spark Operator** is a Kubernetes operator responsible for provisioning,
managing, and cleaning up _ephemeral worker Jobs_ based on reusable templates.
It introduces these Custom Resource Definitions:

- **WorkerTemplate** – defines a reusable Job blueprint
- **WorkerInstance** – creates an actual worker Job from a template, including
  dynamic secret remapping, lifecycle management, and automatic cleanup

This operator is designed for scenarios where multiple, isolated worker Jobs must
be scheduled in a controlled, consistent way — such as distributed workloads,
serverless‑like processing, or per‑request compute workers.

## 📐 Features

- `Jobs` are identified via a custom per-business identifier used as an ephemeral
  namespace
- The resources are lingered until the `Job` is deleted (to allow support or prevent
  temporary recreation). You control the behaviour with `ttlSecondsAfterFinished=0`
  in the `WorkerInstance` resource (when set, it overrides the value of the template's `Job`)

## 🚀 Installation

You can use OLM to deply the operator (currently `controller-operator` does not yet work).

### Install OLM

```sh
kubectl apply -f https://github.com/operator-framework/operator-lifecycle-manager/releases/latest/download/crds.yaml --server-side=true
kubectl apply -f https://github.com/operator-framework/operator-lifecycle-manager/releases/latest/download/olm.yaml --server-side=true
```

### Define the Manifests

Target the proper channel (e.g. `alpha`) and version (e.g. 1.0.10):

```yaml
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: spark-operator-catalog
  namespace: olm
spec:
  sourceType: grpc
  image: docker.io/yextly/spark-operator-catalog:1.0.10
  displayName: Spark Operator Catalog
  publisher: Yextly
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: spark-operator
  namespace: operators
spec:
  channel: alpha
  name: spark-operator
  source: spark-operator-catalog
  sourceNamespace: olm
  installPlanApproval: Automatic
```

### Upgrade

Currently the catalog contains only one operator, therefore upgrading requires the followinf steps

1. Delete the `Subscription`
1. Delete the `CatalogSource`
1. Delete the `CSV` from the `operators` namespace only (the rest is automatically handled)
1. Apply the new `CatalogSource` and `Subscription`

## 🧩 CRD Overview

### WorkerTemplate

```yaml
apiVersion: compute.yextly.io/v1alpha1
kind: WorkerTemplate
metadata:
  name: worker1
  namespace: test-namespace
spec:
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: container1
              image: busybox
              command:
                - /bin/sh
                - -c
                - |
                  echo "Secrets:";
                  for file in /var/secrets/secret1/*; do
                    key=$(basename "$file")
                    value=$(cat "$file")
                    echo "$key: $value"
                  done

              volumeMounts:
                - name: volume1
                  mountPath: /var/secrets/secret1
                  readOnly: true
          volumes:
            - name: volume1
              secret:
                secretName: secret1
```

### WorkerInstance

```yaml
apiVersion: compute.yextly.io/v1alpha1
kind: WorkerInstance
metadata:
  namespace: test-namespace
  name: instance1
spec:
  templateName: worker1
  secrets:
    - metadata:
        name: secret1
      type: Opaque
      data:
        key1: dmFsdWUx
        key2: dmFsdWUy
```

## 📜 WorkerInstance request contract

A `WorkerInstance` must be created in the same namespace as its `WorkerTemplate`.

```yaml
apiVersion: compute.yextly.io/v1alpha1
kind: WorkerInstance
metadata:
  name: instance1               # DNS-1123 subdomain; the default workerId
  namespace: test-namespace     # the WorkerTemplate must be in this namespace
spec:
  templateName: worker1         # required
  workerId: order-4711          # optional; must contain at least one letter or digit
  ttlSecondsAfterFinished: 0    # optional; unset means the Job, instance and copies never expire
  secrets:                      # optional
    - apiVersion: v1            # optional; if present must be "v1"
      kind: Secret              # optional; if present must be "Secret"
      metadata:
        name: secret1           # required, DNS-1123 subdomain, unique in this list
      type: Opaque              # optional; copied
      data:                     # data and/or stringData; copied
        key1: dmFsdWUx
```

### Secrets

For every embedded Secret the operator creates an immutable copy named
`spark-<workerId>-<name>-<hash>` in the instance namespace. The copy carries the labels
`app.kubernetes.io/managed-by: spark-operator` and `compute.yextly.io/workerinstance: <instance>`
and the annotation `yextly.io/associated-to: <instance>`. Any other metadata of the embedded
Secret (namespace, labels, annotations, finalizers) is discarded.

The operator rewrites these references of the template's pod spec to the copies:

- `volumes[].secret.secretName`
- `volumes[].projected.sources[].secret.name`
- `envFrom[].secretRef.name` and `env[].valueFrom.secretKeyRef.name`, in both `containers` and
  `initContainers`

The following cases are checked when the instance is created:

| Case | Outcome |
|---|---|
| Invalid entry (not a Secret object, no or invalid name, duplicated name, wrong `apiVersion`/`kind`) | `Failed`, nothing created |
| `workerId` without any letter or digit | `Failed`, nothing created |
| Embedded name used by `imagePullSecrets` or a CSI `nodePublishSecretRef` (not rewritten) | `Failed`, nothing created |
| Embedded Secret that the template never references | Warning in the `SecretReferences` condition |
| Template reference without an embedded Secret (served by a namespace Secret of that name) | Warning in the `SecretReferences` condition |

Validation failures and warnings are also recorded as events on the `WorkerInstance`
(`kubectl describe workerinstance <name>`). A missing `WorkerTemplate` is retried.

`Failed` is terminal: fix the request, then delete and recreate the instance. Changes to the
spec after the `Job` exists are ignored.

### One live instance per workerId

Instances sharing a `workerId` share the `Job` name and the copy names. A second instance waits
(condition `Blocked`, state `Creating`, retried every 30 seconds) until the previous instance, its
`Job` and its copies are gone; it never uses or deletes another instance's copies.

### Cleanup

When the `Job` disappears (for example through `ttlSecondsAfterFinished`) the instance deletes
itself. Deleting an instance deletes its `Job` with foreground propagation, waits until the `Job`
and its pods are gone, then deletes every copy the instance owns.

### ⚠️ Exposure of embedded values

The embedded values stay, base64-encoded, in the `WorkerInstance` spec for the life of the
instance. Anyone who can `get`, `list` or `watch` `workerinstances` in a namespace, including the
generated `workerinstance-viewer-role`, can read every value: grant that access only where Secret
read access is acceptable. Encryption at rest configured for `secrets` does not cover
`workerinstances.compute.yextly.io`; add the resource to the API server encryption configuration
if the cluster supports encrypting custom resources.

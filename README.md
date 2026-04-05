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
  in the `WorkerTemplate` resource

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
  name: operator
  source: spark-operator-catalog
  sourceNamespace: olm
  installPlanApproval: Automatic
```

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

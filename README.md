# Spark Operator

The **Spark Operator** is a Kubernetes operator responsible for provisioning,
managing, and cleaning up _ephemeral worker Jobs_ based on reusable templates.
It introduces two Custom Resource Definitions:

- **WorkerTemplate** – defines a reusable Job blueprint
- **WorkerInstance** – creates an actual worker Job from a template, including
  dynamic secret remapping, lifecycle management, and automatic cleanup

This operator is designed for scenarios where multiple, isolated worker Jobs must
be scheduled in a controlled, consistent way — such as distributed workloads,
serverless‑like processing, or per‑request compute workers.

## Features

- `Jobs` are identified via a custom per-business identifier used as an ephemeral
  namespace
- The resources are lingered until the `Job` is deleted (to allow support or prevent
  temporary recreation). You control the behaviour with `ttlSecondsAfterFinished=0`
  in the `WorkerTemplate` resource

## 🧩 CRD Overview

### WorkerTemplate

```yaml
apiVersion: compute.yextly.io/v1alpha1
kind: WorkerTemplate
spec:
  jobTemplate:
    # Raw JobTemplateSpec JSON/YAML
```

### WorkerTemplate

```yaml
apiVersion: compute.yextly.io/v1alpha1
kind: WorkerInstance
spec:
  templateName: my-template # required
  workerId: custom-id # optional, defaults to instance name
  ttlSecondsAfterFinished: 0 # optional
  secrets: # optional list of Secret specs
    - apiVersion: v1
      kind: Secret
      metadata:
        name: db-creds
      stringData:
        password: "..."
```

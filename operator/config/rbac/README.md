# RBAC scope

`role.yaml`'s `manager-role` is a `ClusterRole`, not a namespaced `Role` — this is
intentional, not an oversight.

`WorkerInstance`/`WorkerTemplate` custom resources, and the `Job`/`Secret` objects the
controller creates for them, are designed to be created in **any** namespace (each
worker's ephemeral namespace is derived from a caller-supplied business identifier,
per the top-level README's feature list). A namespace-scoped `Role` would have to be
bound in every namespace that might ever host a worker, which defeats the point.

An ArgoCD `AppProject`'s `clusterResourceWhitelist` must allow this `ClusterRole`/
`ClusterRoleBinding` pair for the same reason — see the ArgoCD migration plan.

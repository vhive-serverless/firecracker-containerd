# Released upstream Kubernetes single-node baseline

This is an isolated, ordinary Kubernetes baseline for Step 3. It uses one
upstream v1.37.2 control-plane node with kind v0.33.0, a checksummed official
Kubernetes server archive, and a digest-pinned kind base image. Exact inputs
are in `versions.json` under `selected_inputs.kubernetes_one_node`.

The node uses the kind base's stock containerd v2.3.4 and runc. A successful
baseline proves that the released cluster runs Pods, exec, logs, filesystem
writes and DNS. It does **not** satisfy the Firecracker RuntimeClass,
CRI conformance, or Kubernetes Pod checkpoint/restore acceptance gates.

Run on the designated worker from a committed checkout:

```sh
export STACK_DIR="$HOME/fc-ctrd-mod"
tools/kubernetes/one-node-baseline.sh create
tools/kubernetes/one-node-baseline.sh probe
tools/kubernetes/one-node-baseline.sh delete
```

`create` builds a node image from the verified archive and starts the single
node, then runs `probe`. `probe` records source and binary hashes, node/runtime
versions, API discovery, and the Pod schema. It runs and removes a stock-runtime
test Pod and namespace. Its success marker is
`UPSTREAM_ONE_NODE_BASELINE_OK`. Discovery explicitly reports whether
`PodCheckpoint` and `Pod.spec.restoreFrom` are advertised; baseline success
does not imply either exists.

The API binds to host loopback. Kubeconfig and node ownership state live under
`$STACK_DIR/kubernetes/upstream-one-node`, outside the source checkout. Evidence
directories are printed as `ONE_NODE_LOGS`. Host Kubernetes configuration and
services are not changed. The node remains available after probing; `delete`
checks its recorded Docker container ID and kind cluster label before removal.
Existing clusters with the selected name are rejected by `create`.

The separate `audit-interfaces.sh` checks the requested feature gate against
k3s v1.37.2+k3s1, upstream v1.37.2, and the newer upstream v1.38.0-beta.0 binary. See
[the API audit](step-3-api-audit.md) for the released-source and binary findings.

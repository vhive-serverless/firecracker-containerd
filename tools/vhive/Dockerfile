ARG BASE_IMAGE=localhost/firecracker-containerd-test:latest
FROM ${BASE_IMAGE}
# Legacy vHive captures exactly one root and one workload disk.
RUN sed -i '/^{/a\  "guest_root_mode": "ram",' /etc/containerd/firecracker-runtime.json
COPY vhive-acceptance.test /usr/local/bin/
COPY run-in-container.sh /usr/local/bin/run-vhive-acceptance

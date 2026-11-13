ARG BASE_IMAGE=localhost/firecracker-containerd-test:latest
FROM ${BASE_IMAGE}
# vHive discovers host interfaces using route from net-tools.
RUN apt-get update && apt-get install --yes --no-install-recommends net-tools && \
    command -v route >/dev/null && rm -rf /var/lib/apt/lists/*
COPY vhive-acceptance.test /usr/local/bin/
COPY run-in-container.sh /usr/local/bin/run-vhive-acceptance

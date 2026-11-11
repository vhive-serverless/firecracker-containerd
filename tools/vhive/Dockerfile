ARG BASE_IMAGE=localhost/firecracker-containerd-test:latest
FROM ${BASE_IMAGE}
RUN echo '09a1b669ac6d21e7e24c3c33a9805b48987ba5845e8d88f039c47013a0c0025c  /tmp/containerd.tar.gz' | sha256sum -c - && \
    mkdir -p /etc/vhive-cri /usr/local/libexec/vhive-stock /run/containerd && \
    tar zxf /tmp/containerd.tar.gz -C /usr/local/libexec/vhive-stock --strip-components=1 bin/containerd
COPY vhive vhive-probe /usr/local/bin/
COPY run-in-container.sh /usr/local/bin/run-vhive-acceptance

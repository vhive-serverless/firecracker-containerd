# Guest agent

The agent runs inside the Firecracker microVM and communicates with the host
shim over vsock. It is included in the generated guest image and started by
systemd. These are implemented features, not pending work.

[agent/service.go](../agent/service.go) embeds containerd's runc-v2 task service
and exposes task/v3 operations. The guest runc binary creates and manages OCI
containers. The agent forwards task events, statistics and process stdin/stdout/
stderr between guest processes and the host runtime. The host shim and VMM are
separate host processes; the optional host runc jail is not the guest OCI runtime.

See the [architecture](architecture.md) and [milestone guide](modernization/README.md)
for branch-specific additions and validation. Binary size, guest RSS/PSS and total
VM memory are different metrics; an agent language change alone does not establish
lower VM memory consumption.

# Plain Kubernetes manifests

`sftpgate.yaml` is the Helm chart rendered with its default values, for
clusters where Helm isn't used. It is regenerated with `make manifests`, and
CI fails if it drifts from the chart. For anything beyond the defaults (git-sync,
monitoring, Ingress, several replicas), the Helm chart is easier.

## Install

1. Create the namespace and the host key. Keep the key: partners pin its
   fingerprint, and a new one looks like an attack to them.

   ```sh
   kubectl create namespace sftpgate
   ssh-keygen -t ed25519 -N "" -f ssh_host_ed25519_key
   kubectl -n sftpgate create secret generic sftpgate-host-keys --from-file=ssh_host_ed25519_key
   ```

2. Put your config in the `sftpgate` ConfigMap: edit the `sftpgate.yaml` key in
   `sftpgate.yaml` (this folder), or replace the ConfigMap after applying:

   ```sh
   kubectl -n sftpgate create configmap sftpgate --from-file=sftpgate.yaml=my-config.yaml \
     --dry-run=client -o yaml | kubectl apply -f -
   ```

3. Apply:

   ```sh
   kubectl -n sftpgate apply -f sftpgate.yaml
   kubectl -n sftpgate rollout status statefulset/sftpgate
   kubectl -n sftpgate get service sftpgate    # the address partners connect to
   ```

Config changes are picked up without a restart, usually within a minute.
Secrets referenced as `${env:...}` need to be added to the StatefulSet as
environment variables, and `${file:...}` secrets as mounted volumes. The Helm
chart's `secretEnv` and `secretFiles` values do this for you.

# K3S on STACKIT

## Setup

### on server cilium-lb-1

```
curl -sfL https://get.k3s.io | sh -s - server --cluster-init --disable-network-policy --flannel-backend=none --disable=servicelb,traefik --disable-cloud-controller --kubelet-arg cloud-provider=external
cat /var/lib/rancher/k3s/server/node-token
```

### on server cilium-lb-2 and cilium-lb-3

```
export K3S_TOKEN=<TOKEN>
curl -sfL https://get.k3s.io | sh -s - server --server https://<cilium-lb-1-ip>:6443 --disable-network-policy --flannel-backend=none --disable=servicelb,traefik --disable-cloud-controller --kubelet-arg cloud-provider=external
```

### Access the cluster

copy kubeconfig to your machine and forward with socks5 proxy to jumphost

```
# on node
$ cat /etc/rancher/k3s/k3s.yaml
# on local machine
$ ssh -i ~/.ssh/cilium-lb  -D 127.0.0.1:1337 -N ubuntu@<PUBLIC_IP_JUMPHOST>
$ kubectl config set-cluster default --proxy-url socks5://127.0.0.1:1337
```

### Delete default routes for direct routing device to internet

```bash
ip r del default via 10.0.0.1 dev enp7s0
ip r del 1.1.1.1 via 10.0.0.1 dev enp7s0
```

# Cilium as a LoadBalancer Service

## STACKIT

- Do we support <https://docs.openstack.org/api-ref/network/v2/index.html#ports> `ip-substring-filtering`

```bash
helm upgrade --install cilium cilium/cilium --version 1.18.4 \
   --namespace kube-system \
   --values helm-values.yaml
```

### TODOS

- [ ] remove lock if node has ToBeDeletedByAutoscaler Taint
  - WIP: <https://github.com/hown3d/cilium/commit/71e93043af34e2fd23732a0088e733b8513eff39>
  - Is Coordinated Leader election possible? <https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/4355-coordinated-leader-election/README.md>
- [x] cleanup resources on delete

- [ ] security groups
  -

- [x] geneve DSR
  - ~Currently the SYN ACK packet is somewhere lost~ Must use stateless security groups as packets are dropped if no SYN was first sent

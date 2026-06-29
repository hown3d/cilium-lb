# Cilium LoadBalancer in STACKIT

This project provides a high-performance load balancing solution for Kubernetes workloads using Cilium's eBPF-based networking stack

## Quick Start

### Prerequisites

- STACKIT credentials
- Kubernetes cluster with Cilium installed (version 1.18+)
- Terraform for infrastructure provisioning
- Helm for Cilium configuration

### Deploy Infrastructure

```bash
cd deploy/terraform
terraform init
terraform apply
```

### Deploy components

```bash
make up
kubectl apply -f lbaas.yaml
```

## Key Components

- **LoadBalancer-controller**: Kubernetes controller for managing STACKIT infrastructure
- **Cilium**: Cilium is the backbone for the loadbalancer

## Documentation

For detailed technical documentation, see the [docs/](docs/) folder:

- [Architecture](docs/architecture.md) - Architecture of the LoadBalancer service

## Configuration

Key Cilium settings:

- `loadBalancer.mode: dsr` - Direct Server Return mode
- `loadBalancer.dsrDispatch: geneve` - Geneve encapsulation for DSR
- `tunnelProtocol: geneve` - Geneve for tunneling traffic
- `kubeProxyReplacement: true` - eBPF-based kube proxy

## Todos

- [x] cleanup resources on delete
- [x] Geneve DSR implementation
- [x] Cleanup resources on delete
- [x] STACKIT infrastructure integration
- [x] Security groups
- [ ] find a way to remove the default route for north-south NIC
- [ ] remove lock if node has ToBeDeletedByAutoscaler Taint
  - WIP: <https://github.com/hown3d/cilium/commit/71e93043af34e2fd23732a0088e733b8513eff39>
  - Is Coordinated Leader election possible? <https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/4355-coordinated-leader-election/README.md>

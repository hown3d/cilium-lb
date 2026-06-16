locals {
  project_id = "f5debc67-cfeb-403a-9d92-36fdbe3325c7"
  nodes      = 3
  # ubuntu 22
  image = "686d947e-ac64-453c-87b8-ffe99a1da7c4"
}


terraform {
  required_providers {
    stackit = {
      source  = "stackitcloud/stackit"
      version = "0.80.0"
    }
  }
}



provider "stackit" {
  default_region = "eu01"
}


resource "stackit_key_pair" "this" {
  name       = "cilium-lb"
  public_key = chomp(file("~/.ssh/cilium-lb.pub"))
}

resource "stackit_server" "nodes" {
  count             = local.nodes
  project_id        = local.project_id
  availability_zone = "eu01-${count.index + 1}"

  boot_volume = {
    size        = 50
    source_type = "image"
    source_id   = local.image
  }
  name         = "cilium-lb-${count.index}"
  machine_type = "c2i.2"
  keypair_name = stackit_key_pair.this.name
  network_interfaces = [
    element(stackit_network_interface.default[*].network_interface_id, count.index)
  ]
}


resource "stackit_server_network_interface_attach" "direct_routing" {
  count                = var.enable_direct_routing_interface ? local.nodes : 0
  project_id           = local.project_id
  server_id            = element(stackit_server.nodes[*].server_id, count.index)
  network_interface_id = element(stackit_network_interface.direct-routing[*].network_interface_id, count.index)
}

resource "stackit_server" "jumphost" {
  project_id        = local.project_id
  availability_zone = "eu01-1"
  name              = "cilium-lb-jumphost"
  boot_volume = {
    size        = 10
    source_type = "image"
    source_id   = local.image
  }
  machine_type       = "c2i.1"
  network_interfaces = [stackit_network_interface.jumphost.network_interface_id]
  keypair_name       = "cilium-lb"
}


resource "stackit_network_interface" "jumphost" {
  project_id = local.project_id
  network_id = stackit_network.this.network_id
  security_group_ids = [
    stackit_security_group.ssh.security_group_id,
    stackit_security_group.ssh_internal.security_group_id,
  ]
}

resource "stackit_network_interface" "default" {
  project_id = local.project_id
  count      = local.nodes
  network_id = stackit_network.this.network_id
  allowed_addresses = [
    # pod cidr
    "192.168.0.0/16"
  ]
  security_group_ids = [
    stackit_security_group.default.security_group_id,
    stackit_security_group.ssh_internal.security_group_id,
    stackit_security_group.lbapi.security_group_id,
  ]
  lifecycle {
    ignore_changes = [
      allowed_addresses
    ]
  }
  name = "pod-traffic"
  labels = {
    "type" : "pod-traffic"
  }
}

resource "stackit_security_group" "ssh" {
  project_id = local.project_id
  name       = "ssh"
}

resource "stackit_security_group" "ssh_internal" {
  project_id = local.project_id
  name       = "ssh-internal"
}

resource "stackit_security_group_rule" "ssh" {
  project_id        = local.project_id
  security_group_id = stackit_security_group.ssh.security_group_id
  direction         = "ingress"
  description       = "ssh"
  port_range = {
    max = 22
    min = 22
  }
  protocol = {
    name = "tcp"
  }
  ip_range = "0.0.0.0/0"
}

resource "stackit_security_group_rule" "ssh_icmp" {
  project_id        = local.project_id
  security_group_id = stackit_security_group.ssh.security_group_id
  direction         = "ingress"
  description       = "icmp"
  protocol = {
    name = "icmp"
  }
}


resource "stackit_security_group_rule" "ssh_internal_icmp" {
  project_id        = local.project_id
  security_group_id = stackit_security_group.ssh_internal.security_group_id
  direction         = "ingress"
  protocol = {
    name = "icmp"
  }
  description              = "icmp"
  remote_security_group_id = stackit_security_group.ssh_internal.security_group_id
}

resource "stackit_public_ip" "ssh" {
  project_id           = local.project_id
  network_interface_id = stackit_network_interface.jumphost.network_interface_id
}

resource "stackit_public_ip" "ssh_lb" {
  count                = var.public_ip_on_lb_node ? 1 : 0
  project_id           = local.project_id
  network_interface_id = element(stackit_network_interface.default[*].network_interface_id, 0)
}

resource "stackit_security_group_rule" "ssh_interal" {
  project_id        = local.project_id
  security_group_id = stackit_security_group.ssh_internal.security_group_id
  direction         = "ingress"
  description       = "ssh"
  port_range = {
    max = 22
    min = 22
  }
  protocol = {
    name = "tcp"
  }
  remote_security_group_id = stackit_security_group.ssh_internal.security_group_id
}

resource "stackit_security_group_rule" "apiserver" {
  project_id        = local.project_id
  security_group_id = stackit_security_group.ssh_internal.security_group_id
  direction         = "ingress"
  description       = "apiserver"
  port_range = {
    max = 6443
    min = 6443
  }
  protocol = {
    name = "tcp"
  }
  remote_security_group_id = stackit_security_group.ssh_internal.security_group_id
}



resource "stackit_network_interface" "direct-routing" {
  project_id         = local.project_id
  count              = local.nodes
  network_id         = stackit_network.loadbalancer.network_id
  security_group_ids = [stackit_security_group.dsr.security_group_id]
  labels = {
    "type" : "direct-routing"
  }
  name = "direct-routing"
}


resource "stackit_security_group" "lbapi" {
  project_id = local.project_id
  stateful   = true
  name       = "Traffic from lbapi loadbalancer"
}


resource "stackit_security_group_rule" "lbapi_nodeports" {
  project_id        = local.project_id
  security_group_id = stackit_security_group.lbapi.security_group_id
  description       = "node ports"
  direction         = "ingress"
  protocol = {
    name = "tcp"
  }
  port_range = {
    min = 30000
    max = 32767
  }
}

resource "stackit_security_group" "default" {
  project_id  = local.project_id
  stateful    = true
  name        = "cilium pod traffic"
  description = "cilium pod traffic"
}

resource "stackit_security_group_rule" "default" {
  for_each = {
    "node-to-node ingress" = {
      direction                = "ingress"
      remote_security_group_id = stackit_security_group.default.security_group_id
    }
    "node-to-node egress" = {
      direction                = "egress"
      remote_security_group_id = stackit_security_group.default.security_group_id
    }
    "pod ingress" = {
      direction = "ingress"
      ip_range  = "192.168.0.0/16"
    }
  }
  project_id               = local.project_id
  security_group_id        = stackit_security_group.default.security_group_id
  description              = each.key
  direction                = try(each.value.direction)
  remote_security_group_id = try(each.value.remote_security_group_id, null)
  ip_range                 = try(each.value.ip_range, null)
}



resource "stackit_security_group" "dsr" {
  project_id  = local.project_id
  stateful    = false
  name        = "cilium-dsr"
  description = "cilium dsr"
}

resource "stackit_security_group_rule" "dsr" {
  for_each = {
    "node-to-node ingress" = {
      direction                = "ingress"
      remote_security_group_id = stackit_security_group.dsr.security_group_id
    }
    "node-to-node egress" = {
      direction                = "egress"
      remote_security_group_id = stackit_security_group.dsr.security_group_id
    }
    // as we don't do conntrack, we need to allow the packet to come on any port from any source into the nic
    "scheunentor" = {
      direction = "ingress"
    }
  }
  project_id               = local.project_id
  security_group_id        = stackit_security_group.dsr.security_group_id
  direction                = each.value.direction
  description              = each.key
  remote_security_group_id = try(each.value.remote_security_group_id, null)
}

resource "stackit_network" "this" {
  project_id       = local.project_id
  name             = "cilium-lb"
  ipv4_prefix      = "10.0.0.0/25"
  ipv4_nameservers = ["1.1.1.1"]
}

resource "stackit_network" "loadbalancer" {
  project_id      = local.project_id
  name            = "cilium-lb-north-south"
  ipv4_prefix     = "172.16.0.0/25"
  no_ipv4_gateway = false
  routed          = true
}

output "nics" {
  value = {
    "direct-routing" : [for i, v in stackit_network_interface.direct-routing : { "cilium-lb-${i}" : v.ipv4 }]
    "default" : [for i, v in stackit_network_interface.default : { "cilium-lb-${i}" : v.ipv4 }]
  }
}
output "jumphost_ip" {
  value = stackit_public_ip.ssh.ip
}


output "lb_pub_ip" {
  value = stackit_public_ip.ssh_lb[*].ip
}

output "lb_network" {
  value = stackit_network.loadbalancer.network_id
}

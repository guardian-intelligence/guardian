#!/usr/bin/env python3
"""Render GitHub's Ubuntu Packer build with a QEMU source.

The upstream build owns the provisioner order. This adapter replaces only its
Azure source and final waagent deprovisioning step, then prepends the local
QEMU source and the six variables referenced by the shared build block.
"""

from __future__ import annotations

import argparse
from pathlib import Path

AZURE_SOURCE = '  sources = ["source.azure-arm.image"]'
AZURE_DEPROVISION = """  provisioner "shell" {
    execute_command = "sudo sh -c '{{ .Vars }} {{ .Path }}'"
    inline          = ["sleep 30", "/usr/sbin/waagent -force -deprovision+user && export HISTSIZE=0 && sync"]
  }

"""
BOOTSTRAP_MARKER = '  name = "ubuntu-24_04"\n'
BOOTSTRAP = """

  provisioner "shell" {
    execute_command = "sudo sh -c '{{ .Vars }} {{ .Path }}'"
    inline          = [
      "cloud-init status --wait --long",
      "test -f /etc/waagent.conf"
    ]
  }
"""

PREFIX = r"""
packer {
  required_plugins {
    qemu = {
      source  = "github.com/hashicorp/qemu"
      version = "= ${qemu_plugin_version}"
    }
  }
}

variable "source_image_url" { type = string }
variable "source_image_sha256" { type = string }
variable "output_directory" { type = string }
variable "ssh_private_key_file" { type = string }
variable "cloud_init_meta_data" { type = string }
variable "cloud_init_user_data" { type = string }
variable "qemu_binary" { type = string }
variable "qemu_accelerator" { type = string }
variable "qemu_cpus" { type = number }
variable "qemu_cpu_model" { type = string }
variable "qemu_memory_mib" { type = number }

variable "helper_script_folder" {
  type    = string
  default = "/imagegeneration/helpers"
}
variable "image_folder" {
  type    = string
  default = "/imagegeneration"
}
variable "image_os" {
  type    = string
  default = "ubuntu24"
}
variable "image_version" {
  type = string
}
variable "imagedata_file" {
  type    = string
  default = "/imagegeneration/imagedata.json"
}
variable "installer_script_folder" {
  type    = string
  default = "/imagegeneration/installers"
}

source "qemu" "image" {
  accelerator          = var.qemu_accelerator
  boot_wait            = "5s"
  cd_files             = [var.cloud_init_meta_data, var.cloud_init_user_data]
  cd_label             = "cidata"
  cpus                 = var.qemu_cpus
  cpu_model            = var.qemu_cpu_model
  disk_compression     = true
  disk_discard         = "unmap"
  disk_image           = true
  disk_interface       = "virtio-scsi"
  disk_size            = "80G"
  format               = "qcow2"
  headless             = true
  iso_checksum         = "sha256:${var.source_image_sha256}"
  iso_url              = var.source_image_url
  machine_type         = "q35"
  memory               = var.qemu_memory_mib
  net_device           = "virtio-net"
  output_directory     = var.output_directory
  qemu_binary          = var.qemu_binary
  shutdown_command     = "sudo shutdown -P now"
  ssh_private_key_file = var.ssh_private_key_file
  ssh_timeout          = "30m"
  ssh_username         = "packer"
  vm_name              = "runner-images.qcow2"
}

"""


def render(source: str, plugin_version: str) -> str:
    if source.count(AZURE_SOURCE) != 1:
        raise ValueError("upstream Azure source marker changed")
    if source.count(AZURE_DEPROVISION) != 1:
        raise ValueError("upstream waagent deprovisioner changed")
    if source.count(BOOTSTRAP_MARKER) != 1:
        raise ValueError("upstream Ubuntu build marker changed")

    build = source.replace(AZURE_SOURCE, '  sources = ["source.qemu.image"]')
    build = build.replace(AZURE_DEPROVISION, "")
    build = build.replace(BOOTSTRAP_MARKER, BOOTSTRAP_MARKER + BOOTSTRAP)
    return PREFIX.replace("${qemu_plugin_version}", plugin_version) + build


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--plugin-version", required=True)
    args = parser.parse_args()

    rendered = render(args.source.read_text(), args.plugin_version)
    args.destination.write_text(rendered)


if __name__ == "__main__":
    main()

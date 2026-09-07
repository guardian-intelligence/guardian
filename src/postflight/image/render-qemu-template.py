#!/usr/bin/env python3
"""Render GitHub's Ubuntu Packer build with a QEMU source.

The upstream build owns the provisioner order. This adapter replaces its
Azure source and final waagent deprovisioning step, and isolates pipx from
Ubuntu's apt-managed Python packages before adding the local QEMU source.
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
PYTHON_INSTALLER = '"${path.root}/../scripts/build/install-python.sh"'
PIPX_INSTALL = "python3 -m pip install pipx\npython3 -m pipx ensurepath"
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


def adapt_python_installer(source: str, pipx_version: str) -> str:
    import re

    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", pipx_version):
        raise ValueError("pipx version must be an exact release")
    if source.count(PIPX_INSTALL) != 1:
        raise ValueError("upstream pipx installation changed")
    # pipx now requires packaging>=26, while Noble owns packaging24 through
    # dpkg (no pip RECORD). A venv avoids uninstalling or shadowing that
    # system package and retains the upstream /opt/pipx application layout.
    replacement = f'''python3 -m venv /opt/pipx-bootstrap
/opt/pipx-bootstrap/bin/python -m pip install pipx=={pipx_version}
ln -s /opt/pipx-bootstrap/bin/pipx /usr/local/bin/pipx
/opt/pipx-bootstrap/bin/python -m pipx ensurepath'''
    return source.replace(PIPX_INSTALL, replacement)


def render(source: str, plugin_version: str, python_filename: str) -> str:
    if source.count(AZURE_SOURCE) != 1:
        raise ValueError("upstream Azure source marker changed")
    if source.count(AZURE_DEPROVISION) != 1:
        raise ValueError("upstream waagent deprovisioner changed")
    if source.count(BOOTSTRAP_MARKER) != 1:
        raise ValueError("upstream Ubuntu build marker changed")
    if source.count(PYTHON_INSTALLER) != 1:
        raise ValueError("upstream Python provisioner changed")

    build = source.replace(AZURE_SOURCE, '  sources = ["source.qemu.image"]')
    build = build.replace(AZURE_DEPROVISION, "")
    build = build.replace(BOOTSTRAP_MARKER, BOOTSTRAP_MARKER + BOOTSTRAP)
    build = build.replace(PYTHON_INSTALLER, '"${path.root}/' + python_filename + '"')
    return PREFIX.replace("${qemu_plugin_version}", plugin_version) + build


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--plugin-version", required=True)
    parser.add_argument("--pipx-version", required=True)
    parser.add_argument("--python-installer", required=True, type=Path)
    args = parser.parse_args()

    python_path = args.destination.with_suffix(".install-python.sh")
    python_script = adapt_python_installer(args.python_installer.read_text(), args.pipx_version)
    rendered = render(args.source.read_text(), args.plugin_version, python_path.name)
    python_path.write_text(python_script)
    python_path.chmod(0o755)
    args.destination.write_text(rendered)


if __name__ == "__main__":
    main()

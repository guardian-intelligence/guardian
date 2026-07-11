{{- define "talos.config" }}
{{- if and .TalosVersion (not (semverCompare "<1.12.0-0" .TalosVersion)) }}
{{- include "talos.config.multidoc" . }}
{{- else }}
{{- include "talos.config.legacy" . }}
{{- end }}
{{- end }}

{{- /* Shared machine section: type, nodeLabels (controlplane), kubelet, sysctls, kernel, certSANs, files, install */ -}}
{{- define "talos.config.machine.common" }}
machine:
  {{- if and (eq .MachineType "controlplane") (hasKey (.Values.extraNodeLabels | default dict) "node.kubernetes.io/exclude-from-external-load-balancers") }}
  {{- fail "values.yaml: extraNodeLabels.node.kubernetes.io/exclude-from-external-load-balancers collides with the cozystack preset's control-plane label patch; remove it or fork the preset." }}
  {{- end }}
  {{- if .Values.darkBundleMirror.enabled }}
  # DARK BOOTSTRAP: a dark node has no route to public NTP; the mirror host
  # is the cluster's time source.
  time:
    servers:
      - {{ .Values.darkBundleMirror.timeServer }}
  {{- end }}
  {{- if or (eq .MachineType "controlplane") .Values.extraNodeLabels }}
  nodeLabels:
    {{- if eq .MachineType "controlplane" }}
    node.kubernetes.io/exclude-from-external-load-balancers:
      $patch: delete
    {{- end }}
    {{- with .Values.extraNodeLabels }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  {{- end }}
  {{- with .Values.extraNodeTaints }}
  nodeTaints:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  type: {{ .MachineType }}
  {{- if eq .MachineType "controlplane" }}
  # Scoped credential channel for the etcd-snapshot CronJob (base/backup/):
  # only pods in tenant-root may request Talos API access, and the only role
  # they can hold is os:etcd:backup.
  features:
    kubernetesTalosAPIAccess:
      enabled: true
      allowedRoles:
        - os:etcd:backup
      allowedKubernetesNamespaces:
        - tenant-root
  {{- end }}
  # Host logs (machined, etcd, apid — what `talosctl logs` sees) to the
  # in-cluster receiver (base/observability/talos-log-receiver.yaml,
  # pinned MetalLB IP). Fire-and-forget: a dead receiver drops host logs
  # but never blocks the node.
  logging:
    destinations:
      - endpoint: tcp://10.8.0.200:5170
        format: json_lines
        extraTags:
          node: {{ include "talm.discovered.hostname" . }}
  kubelet:
    nodeIP:
      validSubnets:
        {{- if .Values.advertisedSubnets }}
        {{- toYaml .Values.advertisedSubnets | nindent 8 }}
        {{- else }}
        {{- /* Fall back to the subnet of the node's default-gateway-bearing
               link. cidrNetwork masks host bits so the emitted YAML is the
               canonical network form (192.168.201.0/24) rather than the
               host form (192.168.201.10/24). Dedupe after masking because
               a link with a secondary address in the same subnet would
               otherwise produce duplicate list entries. */ -}}
        {{- $addrs := fromJsonArray (include "talm.discovered.default_addresses_by_gateway" .) }}
        {{- if not $addrs }}
        {{- fail "values.yaml: `advertisedSubnets` was left empty and talm could not derive a default from discovery. No default-gateway-bearing link was found on the node. This field is a cluster-wide subnet selector fed to kubelet and etcd; `talm template` is invoked once per node and cannot merge per-node values into one cluster value. Either set advertisedSubnets explicitly in values.yaml, or ensure the node has a default route before running `talm template`." }}
        {{- end }}
        {{- $subnets := list }}
        {{- range $addrs }}
        {{- $subnets = append $subnets (. | cidrNetwork) }}
        {{- end }}
        {{- range uniq $subnets }}
        - {{ . }}
        {{- end }}
        {{- end }}
    {{- /* extraKubeletExtraArgs MUST NOT collide with the preset's
           built-in extraConfig keys - yaml.v3 (used by Talos config
           decode and by the upgrade-time body write-back) rejects
           duplicate map keys, so a silent merge would emit a config
           that cannot decode. Fail at render with a precise hint
           naming the offending key; operators wanting a different
           default fork the preset. */ -}}
    {{- range $k, $_ := .Values.extraKubeletExtraArgs }}
      {{- if or (eq $k "cpuManagerPolicy") (eq $k "maxPods") }}
        {{- fail (printf "values.yaml: extraKubeletExtraArgs.%s collides with the cozystack preset's built-in kubelet.extraConfig - keys never override (yaml.v3 rejects duplicate map keys on decode). Remove the entry from extraKubeletExtraArgs, or fork the chart preset if you need a different default." $k) }}
      {{- end }}
    {{- end }}
    extraConfig:
      cpuManagerPolicy: static
      maxPods: 512
      {{- with .Values.extraKubeletExtraArgs }}
      {{- toYaml . | nindent 6 }}
      {{- end }}
  {{- /* extraSysctls MUST NOT collide with the preset's built-in
         sysctls; same rationale as extraKubeletExtraArgs. $builtinSysctls
         is the single source of truth for the preset-owned keys - keep
         it in sync with the literal sysctls block rendered further down.

         Always-on DRBD/LINSTOR tuning: Cozystack always runs DRBD (the
         drbd module is loaded unconditionally below), and these knobs
         resolve the TCP-port exhaustion the Cozystack team observed on
         production clusters under DRBD reconnect storms (node reboots,
         resync). tcp_orphan_retries/tcp_fin_timeout speed up reclamation
         of orphaned and FIN-WAIT sockets so a reconnect storm cannot
         outrun cleanup; netdev_* widen the receive backlog so bursty
         replication traffic isn't dropped under load.

         vm.nr_hugepages is treated as preset-owned even when its gate
         (.Values.nr_hugepages) is inactive, so operators always route it
         through the dedicated `nr_hugepages` key. The tcp_keepalive_*
         triplet is preset-owned only while .Values.tcpKeepaliveTuning is
         set (see below), so it can be operator-supplied via extraSysctls
         when the toggle is off. */ -}}
  {{- $builtinSysctls := list
        "vm.nr_hugepages"
        "net.ipv4.neigh.default.gc_thresh1"
        "net.ipv4.neigh.default.gc_thresh2"
        "net.ipv4.neigh.default.gc_thresh3"
        "net.ipv4.tcp_orphan_retries"
        "net.ipv4.tcp_fin_timeout"
        "net.core.netdev_max_backlog"
        "net.core.netdev_budget"
        "net.core.netdev_budget_usecs" }}
  {{- if $.Values.tcpKeepaliveTuning }}
  {{- $builtinSysctls = concat $builtinSysctls (list
        "net.ipv4.tcp_keepalive_time"
        "net.ipv4.tcp_keepalive_intvl"
        "net.ipv4.tcp_keepalive_probes") }}
  {{- end }}
  {{- range $k, $_ := .Values.extraSysctls }}
    {{- if has $k $builtinSysctls }}
      {{- fail (printf "values.yaml: extraSysctls.%s collides with the cozystack preset's built-in machine.sysctls - keys never override (yaml.v3 rejects duplicate map keys on decode). Remove the entry from extraSysctls, or fork the chart preset if you need a different default." $k) }}
    {{- end }}
  {{- end }}
  sysctls:
    {{- with $.Values.nr_hugepages }}
    vm.nr_hugepages: {{ . | quote }}
    {{- end }}
    net.ipv4.neigh.default.gc_thresh1: "4096"
    net.ipv4.neigh.default.gc_thresh2: "8192"
    net.ipv4.neigh.default.gc_thresh3: "16384"
    net.ipv4.tcp_orphan_retries: "3"
    net.ipv4.tcp_fin_timeout: "30"
    net.core.netdev_max_backlog: "5000"
    net.core.netdev_budget: "600"
    net.core.netdev_budget_usecs: "8000"
    {{- if $.Values.tcpKeepaliveTuning }}
    net.ipv4.tcp_keepalive_time: "600"
    net.ipv4.tcp_keepalive_intvl: "10"
    net.ipv4.tcp_keepalive_probes: "6"
    {{- end }}
    {{- with .Values.extraSysctls }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  kernel:
    modules:
    - name: openvswitch
    - name: drbd
      parameters:
        - usermode_helper=disabled
    - name: zfs
    - name: spl
    - name: vfio_pci
    - name: vfio_iommu_type1
    {{- with .Values.extraKernelModules }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  certSANs:
  - 127.0.0.1
  {{- with .Values.certSANs }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
  files:
  - content: |
      [plugins]
        [plugins."io.containerd.grpc.v1.cri"]
          device_ownership_from_security_context = true
        [plugins."io.containerd.cri.v1.runtime"]
          device_ownership_from_security_context = true
    path: /etc/cri/conf.d/20-customization.part
    op: create
  - op: overwrite
    path: /etc/lvm/lvm.conf
    permissions: 0o644
    content: |
      backup {
        backup = 0
        archive = 0
      }
      devices {
         global_filter = [ "r|^/dev/drbd.*|", "r|^/dev/dm-.*|", "r|^/dev/zd.*|", "r|^/dev/loop.*|" ]
      }
  {{- with .Values.extraMachineFiles }}
  {{- toYaml . | nindent 2 }}
  {{- end }}
  install:
    {{- with .Values.image }}
    image: {{ . }}
    {{- end }}
    {{- (include "talm.discovered.disks_info" .) | nindent 4 }}
    {{- include "talos.install.disk_pin" . | trim | nindent 4 }}
{{- end }}

{{- /*
  talos.install.disk_pin emits a stable install-disk pin. Enumeration-ordered
  device names (/dev/nvme0n1) can swap between boots, and install.disk is
  consulted exactly at reimage time — a swapped name installs Talos over the
  wrong disk. Pin by the serial discovery reports for the system disk; fall
  back to the device name only when discovery has no serial to offer
  (offline render, exotic transports).
*/ -}}
{{- define "talos.install.disk_pin" }}
{{- $diskName := include "talm.discovered.system_disk_name" . }}
{{- $serial := "" }}
{{- range (lookup "disks" "" "").items }}
{{- if and (eq .spec.dev_path $diskName) .spec.serial }}
{{- $serial = .spec.serial }}
{{- break }}
{{- end }}
{{- end }}
{{- if $serial }}
diskSelector:
  serial: {{ $serial | quote }}
{{- else }}
disk: {{ $diskName | quote }}
{{- end }}
{{- end }}

{{- /* Shared cluster section */ -}}
{{- define "talos.config.cluster" }}
cluster:
  network:
    cni:
      name: none
    dnsDomain: {{ include "talm.validate.dns1123subdomain" (dict "value" .Values.clusterDomain "field" "clusterDomain") | quote }}
    podSubnets:
      {{- toYaml .Values.podSubnets | nindent 6 }}
    serviceSubnets:
      {{- toYaml .Values.serviceSubnets | nindent 6 }}
  clusterName: {{ include "talm.validate.dns1123subdomain" (dict "value" (.Values.clusterName | default .Chart.Name) "field" "clusterName") | quote }}
  controlPlane:
    endpoint: {{ required "values.yaml: `endpoint` must be set to the cluster control-plane URL (e.g. https://<vip>:6443). This field is cluster-wide: every node's kubelet and kube-proxy dials it, so it cannot be auto-derived from the current node's IP -- `talm template` runs once per node and has no way to reconcile per-node IPs into a single shared endpoint. For multi-node setups use a VIP (cozystack floatingIP) or an external load balancer; for single-node clusters the node's routable IP works." .Values.endpoint | quote }}
  {{- if eq .MachineType "controlplane" }}
  {{- with .Values.adminKubeconfigCertLifetime }}
  adminKubeconfig:
    certLifetime: {{ . | quote }}
  {{- end }}
  allowSchedulingOnControlPlanes: true
  controllerManager:
    extraArgs:
      bind-address: 0.0.0.0
      {{- if .Values.allocateNodeCIDRs }}
      allocate-node-cidrs: true
      cluster-cidr: "{{ join "," .Values.podSubnets }}"
      {{- else }}
      allocate-node-cidrs: false
      {{- end }}
  scheduler:
    extraArgs:
      bind-address: 0.0.0.0
  apiServer:
    {{- if and .Values.oidcIssuerUrl (ne .Values.oidcIssuerUrl "") }}
    extraArgs:
      oidc-issuer-url: "{{ .Values.oidcIssuerUrl }}"
      oidc-client-id: "kubernetes"
      oidc-username-claim: "preferred_username"
      oidc-groups-claim: "groups"
    {{- end }}
    certSANs:
    - 127.0.0.1
    {{- with .Values.certSANs }}
    {{- toYaml . | nindent 4 }}
    {{- end }}
  proxy:
    disabled: true
  discovery:
    enabled: false
  etcd:
    advertisedSubnets:
      {{- if .Values.advertisedSubnets }}
      {{- toYaml .Values.advertisedSubnets | nindent 6 }}
      {{- else }}
      {{- /* Fall back to the subnet of the node's default-gateway-bearing
             link; cidrNetwork masks host bits to emit canonical network
             form. Dedupe handled the same way as validSubnets above.
             Empty discovery already errored via validSubnets' required()
             guard, so we reach this block only when at least one address
             was resolved. */ -}}
      {{- $subnets := list }}
      {{- range fromJsonArray (include "talm.discovered.default_addresses_by_gateway" .) }}
      {{- $subnets = append $subnets (. | cidrNetwork) }}
      {{- end }}
      {{- range uniq $subnets }}
      - {{ . }}
      {{- end }}
      {{- end }}
    {{/* Cozystack's monitoring-agents etcd proxy runs host-networked on
         every control-plane node and reads localhost:2381. Keep etcd's
         unauthenticated listener on that shared host loopback; the proxy
         authenticates vmagent before exposing the scrape. */}}
    extraArgs:
      listen-metrics-urls: {{ required "values.yaml: `etcd.metricsListenURL` must match the monitoring-agents etcd proxy upstream" (index (.Values.etcd | default dict) "metricsListenURL") | quote }}
      metrics: {{ required "values.yaml: `etcd.metricsLevel` must expose the histogram consumed by Cozystack's etcd gRPC latency alert" (index (.Values.etcd | default dict) "metricsLevel") | quote }}
    {{- /* etcd backend quota, tunable via values. Raises etcd's 2GiB
           default backend ceiling so a LINSTOR-heavy control plane -
           thousands of DRBD-resource CRDs in aggregate - does not trip
           etcd's NOSPACE alarm and drop into read-only mode. This is a
           ceiling, not a reservation: a small cluster's DB stays small
           and costs no extra RAM/disk. 8GiB is etcd's documented upper
           bound (it warns above that). Blank the value to fall back to
           etcd's own default. Note: this governs total DB size, not the
           size of any single object - per-object writes are still gated
           by kube-apiserver's fixed 3MiB request-body limit. */ -}}
    {{- with (.Values.etcd | default dict).quotaBackendBytes }}
      quota-backend-bytes: {{ . | quote }}
    {{- end }}
  {{- end }}
{{- end }}

{{- /* Shared network document generation for v1.12+ multi-doc format */ -}}
{{- define "talos.config.network.multidoc" }}
{{- include "talm.config.network.multidoc" . }}
{{- end }}

{{- /* Shared legacy network section for machine.network */ -}}
{{- define "talos.config.network.legacy" }}
{{- /* Coerce floatingIP through toString and call the shared
       talm.validate_floatingIP partial so legacy renders fail at
       template time on a malformed value, same as the multi-doc
       path. $fipStr / $fipIsSet are reused below in place of every
       direct .Values.floatingIP reference. */ -}}
{{- $fipStr := .Values.floatingIP | toString }}
{{- $fipIsSet := and (ne $fipStr "") (ne $fipStr "<nil>") }}
{{- include "talm.validate_floatingIP" . }}
  network:
    hostname: {{ include "talm.discovered.hostname" . | quote }}
    nameservers: {{ include "talm.discovered.default_resolvers" . }}
    {{- (include "talm.discovered.physical_links_info" .) | nindent 4 }}
    {{- $existingInterfacesConfiguration := include "talm.discovered.existing_interfaces_configuration" . }}
    {{- $defaultLinkName := include "talm.discovered.default_link_name_by_gateway" . }}
    {{- /* vipLink override on the legacy schema: legacy Talos has no
       Layer2VIPConfig document, so the override is expressed as a
       top-level interfaces[] entry that carries only the vip block.
       When vipLink == $defaultLinkName the inline vip below already
       lands on the right link, so no override entry is needed. */}}
    {{- $vipOverride := and $fipIsSet .Values.vipLink (eq .MachineType "controlplane") (ne .Values.vipLink $defaultLinkName) }}
    {{- /* Suppress the inline (discovery-derived) vip when the operator
       has redirected it to a different link; otherwise the VIP would
       be pinned twice on different interfaces. */}}
    {{- $suppressInlineVip := and .Values.vipLink (ne .Values.vipLink $defaultLinkName) }}
    {{- if or $existingInterfacesConfiguration $defaultLinkName $vipOverride }}
    interfaces:
    {{- if $existingInterfacesConfiguration }}
    {{- $existingInterfacesConfiguration | nindent 4 }}
    {{- else if $defaultLinkName }}
    {{- $isVlan := include "talm.discovered.is_vlan" $defaultLinkName }}
    {{- $parentLinkName := "" }}
    {{- if $isVlan }}
    {{- $parentLinkName = include "talm.discovered.parent_link_name" $defaultLinkName }}
    {{- end }}
    {{- $interfaceName := $defaultLinkName }}
    {{- if and $isVlan $parentLinkName }}
    {{- $interfaceName = $parentLinkName }}
    {{- end }}
    - interface: {{ $interfaceName }}
      {{- $bondConfig := include "talm.discovered.bond_config" $interfaceName }}
      {{- if $bondConfig }}
      {{- $bondConfig | nindent 6 }}
      {{- end }}
      {{- if $isVlan }}
      vlans:
        - vlanId: {{ include "talm.discovered.vlan_id" $defaultLinkName }}
          addresses: {{ include "talm.discovered.default_addresses_by_gateway" . }}
          routes:
            - network: 0.0.0.0/0
              gateway: {{ include "talm.discovered.default_gateway" . }}
          {{- if and $fipIsSet (eq .MachineType "controlplane") (not $suppressInlineVip) }}
          vip:
            ip: {{ $fipStr }}
          {{- end }}
      {{- else }}
      addresses: {{ include "talm.discovered.default_addresses_by_gateway" . }}
      routes:
        - network: 0.0.0.0/0
          gateway: {{ include "talm.discovered.default_gateway" . }}
      {{- if and $fipIsSet (eq .MachineType "controlplane") (not $suppressInlineVip) }}
      vip:
        ip: {{ $fipStr }}
      {{- end }}
      {{- end }}
    {{- end }}
    {{- if $vipOverride }}
    - interface: {{ .Values.vipLink }}
      vip:
        ip: {{ $fipStr }}
    {{- end }}
    {{- end }}
{{- end }}

{{- define "talos.config.legacy" }}
{{- include "talos.config.machine.common" . }}
  registries:
    mirrors:
{{- if .Values.darkBundleMirror.enabled }}
      # DARK BOOTSTRAP: every locked upstream is served from the haul mirror
      # and nothing may fall back to the internet. Entered/exited via PRs.
{{- range .Values.darkBundleMirror.registries }}
      {{ . }}:
        endpoints:
        - {{ trimSuffix "/" $.Values.darkBundleMirror.endpoint }}
        skipFallback: true
{{- end }}
{{- else }}
      docker.io:
        endpoints:
        - https://mirror.gcr.io
{{- end }}
{{- include "talos.config.network.legacy" . }}

{{- include "talos.config.cluster" . }}
{{- end }}

{{- define "talos.config.multidoc" }}
{{- include "talos.config.machine.common" . }}

{{- include "talos.config.cluster" . }}
{{- if .Values.darkBundleMirror.enabled }}
{{- range .Values.darkBundleMirror.registries }}
---
# DARK BOOTSTRAP: served from the haul mirror; skipFallback makes any miss
# fail loudly instead of silently reaching the internet.
apiVersion: v1alpha1
kind: RegistryMirrorConfig
name: {{ . }}
endpoints:
  - url: {{ trimSuffix "/" $.Values.darkBundleMirror.endpoint }}
skipFallback: true
{{- end }}
{{- else }}
---
apiVersion: v1alpha1
kind: RegistryMirrorConfig
name: docker.io
endpoints:
  - url: https://mirror.gcr.io
---
# Steady-state ghcr.io mirror: the in-cluster zot pull-through tier
# (deployments/guardian/system/zot-helmrelease.yaml pins the VIP; design in
# docs/registry-design.md). The VIP is deliberately the ONLY endpoint:
# containerd falls back to upstream ghcr implicitly on a miss or outage,
# and listing the upstream explicitly would disable that fallback and
# deadlock pulls on a mirror 404.
apiVersion: v1alpha1
kind: RegistryMirrorConfig
name: ghcr.io
endpoints:
  - url: http://10.8.0.201:5000
{{- end }}
---
# Arm the chipset watchdog (SP5100 TCO on the Latitude nodes). machined pets
# the device; a hard hang (kernel lockup, machined freeze) stops the petting
# and the hardware reboots the node with no software cooperation.
apiVersion: v1alpha1
kind: WatchdogTimerConfig
device: /dev/watchdog0
timeout: 1m
---
# Host ingress firewall: default-deny. Talos rate-limits ICMP itself but
# does NOT exempt pod-sourced traffic (proven live: pod→host SYNs arrive
# on ovn0 with pod-subnet sources and are dropped), so the cluster fabric
# is admitted explicitly: the Latitude VLAN (etcd, DRBD/LINSTOR, geneve,
# MetalLB memberlist), the pod subnets (pod→apid for etcd snapshots,
# pod→host scrapes), and kube-ovn's join subnet (ovn0-originated
# gateway traffic). All three are private to these machines. The public
# interface admits only the Cloudflare-fronted edge ports and the
# operator subnets. A wrong rule can sever apid: apply with --mode=try
# first; lockout recovery is the Latitude OOB console.
apiVersion: v1alpha1
kind: NetworkDefaultActionConfig
ingress: block
{{- range $proto := list "tcp" "udp" }}
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: cluster-internal-{{ $proto }}
portSelector:
  ports:
    - 1-65535
  protocol: {{ $proto }}
ingress:
  - subnet: {{ $.Values.ingressFirewall.vlanSubnet }}
  {{- range $.Values.podSubnets }}
  - subnet: {{ . }}
  {{- end }}
  - subnet: {{ $.Values.ingressFirewall.joinSubnet }}
{{- end }}
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: operator-talos-api
portSelector:
  ports:
    - 50000
  protocol: tcp
ingress:
  {{- range .Values.ingressFirewall.operatorSubnets }}
  - subnet: {{ . }}
  {{- end }}
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: operator-kubernetes-api
portSelector:
  ports:
    - 6443
  protocol: tcp
ingress:
  {{- range .Values.ingressFirewall.operatorSubnets }}
  - subnet: {{ . }}
  {{- end }}
---
apiVersion: v1alpha1
kind: NetworkRuleConfig
name: public-edge
portSelector:
  ports:
    - 80
    - 443
  protocol: tcp
ingress:
  - subnet: 0.0.0.0/0
  - subnet: ::/0
{{- include "talos.config.network.multidoc" . }}
{{- end }}

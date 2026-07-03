#!/usr/bin/env bash
# Render or remove a company-site PR preview in a checkout of the `previews`
# orchestration branch. Called by company-site-preview.yml (render) and
# company-site-preview-teardown.yml (remove); runnable locally the same way.
#
#   company-site-preview.sh render <previews-checkout> <pr-number> <image-digest> <head-sha>
#   company-site-preview.sh remove <previews-checkout> <pr-number>
#
# The branch layout is machine-managed:
#   manifests/kustomization.yaml   regenerated here from the pr-* dirs
#   manifests/preview-index.yaml   static seed ConfigMap (never rewritten)
#   manifests/pr-<N>/              one preview: Deployment/Service/Ingress/DNSEndpoint
set -euo pipefail

die() { echo "company-site-preview: $*" >&2; exit 1; }

regen_root_kustomization() {
  local root="$1"
  {
    echo "# Machine-managed by .github/scripts/company-site-preview.sh — do not edit."
    echo "apiVersion: kustomize.config.k8s.io/v1beta1"
    echo "kind: Kustomization"
    echo "resources:"
    echo "  - preview-index.yaml"
    for dir in "$root"/pr-*/; do
      [ -d "$dir" ] || continue
      echo "  - $(basename "$dir")"
    done
  } > "$root/kustomization.yaml"
}

render() {
  local tree="$1" pr="$2" digest="$3" sha="$4"
  [[ "$pr" =~ ^[0-9]+$ ]] || die "pr-number must be numeric, got: $pr"
  [[ "$digest" =~ ^sha256:[a-f0-9]{64}$ ]] || die "image-digest must be sha256:<64 hex>, got: $digest"
  [[ "$sha" =~ ^[a-f0-9]{40}$ ]] || die "head-sha must be a full 40-hex sha, got: $sha"

  local name="company-site-pr-${pr}"
  local host="pr-${pr}.guardianintelligence.org"
  local dir="$tree/manifests/pr-${pr}"
  mkdir -p "$dir"

  cat > "$dir/kustomization.yaml" << EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - web.yaml
EOF

  cat > "$dir/web.yaml" << EOF
# Machine-managed preview of company-site for PR #${pr} (head ${sha}).
# Shape mirrors deployments/company/prod/web.yaml, minus the PDB, at 1 replica.
# The shared app.kubernetes.io/name: company-site-preview label is what the
# static Cilium admit pair on main matches; guardian.dev/preview scopes the
# selector to this PR.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${name}
  namespace: tenant-guardian-previews
  labels:
    app.kubernetes.io/name: company-site-preview
    app.kubernetes.io/part-of: guardian
    app.kubernetes.io/component: web
    guardian.dev/product: company
    guardian.dev/stage: previews
    guardian.dev/preview: pr-${pr}
spec:
  replicas: 1
  revisionHistoryLimit: 2
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 0
      maxSurge: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: company-site-preview
      guardian.dev/preview: pr-${pr}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: company-site-preview
        app.kubernetes.io/part-of: guardian
        app.kubernetes.io/component: web
        guardian.dev/product: company
        guardian.dev/stage: previews
        guardian.dev/preview: pr-${pr}
    spec:
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: web
          image: ghcr.io/guardian-intelligence/company-site@${digest}
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 8080
              protocol: TCP
          env:
            - name: GUARDIAN_SITE
              value: pr-${pr}
            - name: GUARDIAN_COMMIT_SHA
              value: "${sha}"
            - name: GUARDIAN_DEPLOY_ID
              valueFrom:
                fieldRef:
                  fieldPath: metadata.uid
          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            periodSeconds: 5
            timeoutSeconds: 2
            failureThreshold: 3
          livenessProbe:
            httpGet:
              path: /livez
              port: http
            initialDelaySeconds: 10
            periodSeconds: 10
            timeoutSeconds: 2
            failureThreshold: 3
          resources:
            requests:
              cpu: 25m
              memory: 128Mi
              ephemeral-storage: 128Mi
            limits:
              cpu: 500m
              memory: 512Mi
              ephemeral-storage: 1Gi
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
---
apiVersion: v1
kind: Service
metadata:
  name: ${name}
  namespace: tenant-guardian-previews
  labels:
    app.kubernetes.io/name: company-site-preview
    app.kubernetes.io/part-of: guardian
    guardian.dev/product: company
    guardian.dev/stage: previews
    guardian.dev/preview: pr-${pr}
spec:
  type: ClusterIP
  internalTrafficPolicy: Cluster
  selector:
    app.kubernetes.io/name: company-site-preview
    guardian.dev/preview: pr-${pr}
  ports:
    - name: http
      port: 80
      targetPort: http
      protocol: TCP
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ${name}
  namespace: tenant-guardian-previews
  labels:
    app.kubernetes.io/name: company-site-preview
    app.kubernetes.io/part-of: guardian
    guardian.dev/product: company
    guardian.dev/stage: previews
    guardian.dev/preview: pr-${pr}
spec:
  ingressClassName: tenant-root
  rules:
    - host: ${host}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: ${name}
                port:
                  number: 80
---
# external-dns (sources: [crd]) turns this into a Cloudflare-proxied CNAME to
# the apex: same edge cert, same origins, zero per-PR Terraform. policy: sync
# deletes the record when Flux prunes this object on teardown.
apiVersion: externaldns.k8s.io/v1alpha1
kind: DNSEndpoint
metadata:
  name: ${name}
  namespace: tenant-guardian-previews
  labels:
    app.kubernetes.io/name: company-site-preview
    app.kubernetes.io/part-of: guardian
    guardian.dev/product: company
    guardian.dev/preview: pr-${pr}
spec:
  endpoints:
    - dnsName: ${host}
      recordType: CNAME
      targets:
        - guardianintelligence.org
      providerSpecific:
        - name: external-dns.alpha.kubernetes.io/cloudflare-proxied
          value: "true"
EOF

  regen_root_kustomization "$tree/manifests"
  echo "rendered $dir (image @${digest})"
}

remove() {
  local tree="$1" pr="$2"
  [[ "$pr" =~ ^[0-9]+$ ]] || die "pr-number must be numeric, got: $pr"
  rm -rf "$tree/manifests/pr-${pr}"
  regen_root_kustomization "$tree/manifests"
  echo "removed manifests/pr-${pr}"
}

cmd="${1:-}"; shift || true
case "$cmd" in
  render) [ $# -eq 4 ] || die "render needs <previews-checkout> <pr-number> <image-digest> <head-sha>"; render "$@" ;;
  remove) [ $# -eq 2 ] || die "remove needs <previews-checkout> <pr-number>"; remove "$@" ;;
  *) die "usage: company-site-preview.sh render|remove ..." ;;
esac

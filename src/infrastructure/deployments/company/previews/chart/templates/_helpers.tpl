{{- define "preview.pr" -}}
{{- required "values.pr (PR number) is required" .Values.pr -}}
{{- end }}

{{- define "preview.name" -}}
company-site-pr-{{ include "preview.pr" . }}
{{- end }}

{{- define "preview.host" -}}
pr-{{ include "preview.pr" . }}.{{ .Values.domain }}
{{- end }}

{{/* The shared app.kubernetes.io/name is what the static Cilium admit pair
     on main matches; guardian.dev/preview scopes selectors to this PR. */}}
{{- define "preview.selector" -}}
app.kubernetes.io/name: company-site-preview
guardian.dev/preview: pr-{{ include "preview.pr" . }}
{{- end }}

{{- define "preview.labels" -}}
{{ include "preview.selector" . }}
app.kubernetes.io/part-of: guardian
guardian.dev/product: company
guardian.dev/stage: previews
{{- end }}

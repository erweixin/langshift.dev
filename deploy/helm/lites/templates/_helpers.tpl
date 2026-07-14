{{- define "lites.fullname" -}}
{{- default .Chart.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "lites.workloadName" -}}
{{- printf "%s-%s" (include "lites.fullname" .root) .name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "lites.labels" -}}
app.kubernetes.io/name: {{ include "lites.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/version: {{ required "global.version is required" .Values.global.version | quote }}
app.kubernetes.io/part-of: lites-cloud-agent
lites.dev/cell: {{ required "global.cell is required" .Values.global.cell | quote }}
lites.dev/region: {{ required "global.region is required" .Values.global.region | quote }}
{{- end -}}

{{- define "lites.selectorLabels" -}}
app.kubernetes.io/name: {{ include "lites.fullname" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .name }}
{{- end -}}

{{- define "lites.image" -}}
{{- $digest := required (printf "workloads.%s.image.digest is required" .name) .workload.image.digest -}}
{{- if not (regexMatch "^sha256:[0-9a-f]{64}$" $digest) -}}{{ fail (printf "workloads.%s.image.digest must be sha256" .name) }}{{- end -}}
{{- printf "%s/%s@%s" (required "global.imageRegistry is required" .root.Values.global.imageRegistry) (required (printf "workloads.%s.image.repository is required" .name) .workload.image.repository) $digest -}}
{{- end -}}

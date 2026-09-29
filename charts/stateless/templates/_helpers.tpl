{{/*
Application name is the Helm release (the Kuberpack app name).
*/}}
{{- define "stateless.fullname" -}}
{{- default .Release.Name .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "stateless.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{ include "stateless.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: kuberpack
{{- end }}

{{- define "stateless.selectorLabels" -}}
app.kubernetes.io/name: {{ include "stateless.fullname" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

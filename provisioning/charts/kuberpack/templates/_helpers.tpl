{{- define "kuberpack.labels" -}}
app.kubernetes.io/name: kuberpack
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

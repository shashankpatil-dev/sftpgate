{{/* Name of the release's resources, like "sftpgate". */}}
{{- define "sftpgate.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/* Labels every resource gets. */}}
{{- define "sftpgate.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{/* Labels that pick out the SFTPgate pods. */}}
{{- define "sftpgate.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "sftpgate.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "sftpgate.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Where SFTPgate reads its config. */}}
{{- define "sftpgate.configPath" -}}
{{- if .Values.gitSync.enabled -}}
/git/current/{{ .Values.gitSync.path }}
{{- else -}}
/etc/sftpgate/config/sftpgate.yaml
{{- end -}}
{{- end -}}

{{/* git-sync settings, shared by the first clone and the sidecar. */}}
{{- define "sftpgate.gitSyncEnv" -}}
- name: GITSYNC_REPO
  value: {{ required "gitSync.repo is required when gitSync.enabled is true" .Values.gitSync.repo | quote }}
- name: GITSYNC_REF
  value: {{ .Values.gitSync.ref | quote }}
- name: GITSYNC_ROOT
  value: /git
- name: GITSYNC_LINK
  value: current
- name: GITSYNC_PERIOD
  value: {{ .Values.gitSync.period | quote }}
{{- with .Values.gitSync.extraEnv }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{/* Locked-down container settings, used for every container. */}}
{{- define "sftpgate.containerSecurity" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
capabilities:
  drop: [ALL]
{{- end -}}

{{/*
Expand the name of the chart.
*/}}
{{- define "application.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}




{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "application.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Generate labels.
*/}}
{{- define "application.labels" -}}
helm.sh/chart: {{ template "application.chart" . }}
{{ include "application.selectorLabels" . }}
app.kubernetes.io/part-of: {{ template "application.name" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion }}
{{- end }}
{{- if .Values.commonLabels}}
{{ toYaml .Values.commonLabels }}
{{- end }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "application.selectorLabels" -}}
app.kubernetes.io/name: {{ template "application.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
HA preflight: replicas > 1 requires a shared backend (S3 or NFS RWX).
Local PVC is RWO and cannot be mounted on multiple nodes simultaneously.
*/}}
{{- if and (gt (int .Values.replicas) 1) (not .Values.s3.enabled) (not .Values.nfs.enabled) -}}
{{- fail "replicas > 1 requires either s3.enabled=true OR nfs.enabled=true (RWX). A local PVC (ReadWriteOnce) cannot be shared across replicas." -}}
{{- end -}}

{{/*
HA preflight: replicas > 1 + Redis disabled is unsafe.
Without Redis, distributed locks (index updates, scan decisions, GC) are noop and races can occur.
Single-flight on proxy pulls is also disabled. We allow it but warn loudly.
*/}}
{{- if and (gt (int .Values.replicas) 1) (not .Values.redis.enabled) -}}
{{- /* warning printed via NOTES, not a hard fail */ -}}
{{- end -}}

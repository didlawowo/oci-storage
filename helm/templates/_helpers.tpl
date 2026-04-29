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

{{/*
Convert the first PVC's `size` (e.g. "200Gi", "500M", "2Ti") to bytes.
Exposed via STORAGE_QUOTA_BYTES env var so the app can report a meaningful disk %
even when statfs() lies (NFS mounts always report the underlying server volume size).
Returns 0 (= "unknown", UI hides the bar) if no PVC is declared or size unparseable.
*/}}
{{- define "oci-storage.storageQuotaBytes" -}}
{{- $size := "" -}}
{{- if .Values.persistentVolumesClaims -}}
  {{- $size = (index .Values.persistentVolumesClaims 0).size -}}
{{- end -}}
{{- if not $size -}}0{{- else -}}
  {{- /* Strip the suffix and multiply. Helm's `int64` returns 0 on failure. */ -}}
  {{- if hasSuffix "Ki" $size -}}{{- mul (trimSuffix "Ki" $size | int64) 1024 -}}
  {{- else if hasSuffix "Mi" $size -}}{{- mul (trimSuffix "Mi" $size | int64) 1048576 -}}
  {{- else if hasSuffix "Gi" $size -}}{{- mul (trimSuffix "Gi" $size | int64) 1073741824 -}}
  {{- else if hasSuffix "Ti" $size -}}{{- mul (trimSuffix "Ti" $size | int64) 1099511627776 -}}
  {{- else if hasSuffix "K" $size -}}{{- mul (trimSuffix "K" $size | int64) 1000 -}}
  {{- else if hasSuffix "M" $size -}}{{- mul (trimSuffix "M" $size | int64) 1000000 -}}
  {{- else if hasSuffix "G" $size -}}{{- mul (trimSuffix "G" $size | int64) 1000000000 -}}
  {{- else if hasSuffix "T" $size -}}{{- mul (trimSuffix "T" $size | int64) 1000000000000 -}}
  {{- else -}}{{- $size | int64 -}}{{- end -}}
{{- end -}}
{{- end -}}

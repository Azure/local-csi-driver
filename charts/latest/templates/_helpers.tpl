{{/*
Expand the name of the chart.
*/}}
{{- define "chart.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "chart.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "chart.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "chart.labels" -}}
helm.sh/chart: {{ include "chart.chart" . }}
{{ include "chart.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "chart.selectorLabels" -}}
app.kubernetes.io/name: {{ include "chart.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "chart.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "chart.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Generate nodeAffinity with mandatory OS/arch constraints.
This ensures the CSI driver only runs on supported platforms while allowing
users to add their own node selection criteria.
*/}}
{{- define "chart.nodeAffinity" -}}
nodeAffinity:
  {{- if .Values.daemonset.nodeAffinity }}
  {{- if .Values.daemonset.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution }}
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
    {{- range .Values.daemonset.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms }}
    - matchExpressions:
      {{- if .matchExpressions }}
      {{- range .matchExpressions }}
      - key: {{ .key }}
        operator: {{ .operator }}
        values: {{- toYaml .values | nindent 10 }}
      {{- end }}
      {{- end }}
      # Add mandatory OS/arch constraints to each term
      - key: kubernetes.io/os
        operator: In
        values:
        - linux
      - key: kubernetes.io/arch
        operator: In
        values:
        - amd64
        - arm64
      {{- if .matchFields }}
      matchFields:
      {{- range .matchFields }}
      - key: {{ .key }}
        operator: {{ .operator }}
        values: {{- toYaml .values | nindent 10 }}
      {{- end }}
      {{- end }}
    {{- end }}
  {{- end }}
  {{- if .Values.daemonset.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution }}
  preferredDuringSchedulingIgnoredDuringExecution:
  {{- toYaml .Values.daemonset.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution | nindent 4 }}
  {{- end }}
  {{- else }}
  requiredDuringSchedulingIgnoredDuringExecution:
    nodeSelectorTerms:
    # Default mandatory constraints when no custom nodeAffinity provided
    - matchExpressions:
      - key: kubernetes.io/os
        operator: In
        values:
        - linux
      - key: kubernetes.io/arch
        operator: In
        values:
        - amd64
        - arm64
  {{- end }}
{{- end }}

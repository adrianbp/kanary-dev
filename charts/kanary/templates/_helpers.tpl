{{/*
Expand the name of the chart.
*/}}
{{- define "kanary.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kanary.fullname" -}}
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
Create chart label.
*/}}
{{- define "kanary.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "kanary.labels" -}}
helm.sh/chart: {{ include "kanary.chart" . }}
{{ include "kanary.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "kanary.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kanary.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "kanary.serviceAccountName" -}}
{{- include "kanary.fullname" . }}
{{- end }}

{{/*
Image reference.
*/}}
{{- define "kanary.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

{{/*
--watch-namespaces flag value.
  cluster        → empty string (cluster-wide)
  namespaced     → release namespace
  multi-namespace → comma-joined list from .Values.scope.namespaces
*/}}
{{- define "kanary.watchNamespaces" -}}
{{- if eq .Values.scope.mode "cluster" -}}
{{- "" -}}
{{- else if eq .Values.scope.mode "namespaced" -}}
{{- .Release.Namespace -}}
{{- else if eq .Values.scope.mode "multi-namespace" -}}
{{- join "," .Values.scope.namespaces -}}
{{- end -}}
{{- end }}

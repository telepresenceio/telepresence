{{/*
Expand the name of the chart.
*/}}
{{- define "telepresence.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
{{- define "telepresence.fullname" -}}
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
*/}}

{{- define "telepresence.fullname" -}}
{{- $name := default "traffic-manager" }}
{{- if .Values.isCI }}
{{- print "traffic-agent" }}
{{- else }}
{{- if ne $name .Release.Name }}
{{- fail "The name of the release MUST BE traffic-manager" }}
{{- end }}
{{- printf "%s" .Release.Name }}
{{- end -}}
{{- end -}}

{{/*
Traffic Manager Namespace
*/}}
{{- define "telepresence.namespace" -}}
{{- if .Values.isCI }}
{{- print "ambassador" }}
{{- else }}
{{- if ne "ambassador" .Release.Namespace}}
{{- fail "The Traffic Manager MUST BE deployed to the namespace named Ambassador" }}
{{- end }}
{{- printf "%s" .Release.Namespace }}
{{- end }}
{{- end -}}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "telepresence.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "telepresence.labels" -}}
{{ include "telepresence.selectorLabels" . }}
helm.sh/chart: {{ include "telepresence.chart" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "telepresence.selectorLabels" -}}
app: traffic-manager
telepresence: manager
{{- end }}

{{/*
RBAC name suffix
*/}}
{{- define "telepresence.rbacName" -}}
{{ default (include "telepresence.name" .) .Values.rbac.nameOverride }}
{{- end -}}

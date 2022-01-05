{{/*
Expand the name of the chart.
*/}}
{{- define "telepresence.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "telepresence.fullname" -}}
{{- $name := default "traffic-manager" }}
{{- if .Values.isCI }}
{{- print $name }}
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
{{/* This value is intentionally undocumented -- it's used by the telepresence binary to determine ownership of the release */}}
{{- if .Values.createdBy }}
app.kubernetes.io/created-by: {{ .Values.createdBy }}
{{- else }}
app.kubernetes.io/created-by: {{ .Release.Service }}
{{- end }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "telepresence.selectorLabels" -}}
app: traffic-manager
telepresence: manager
{{- end }}

{{/*
Client RBAC name suffix
*/}}
{{- define "telepresence.clientRbacName" -}}
{{ printf "%s-%s" (default (include "telepresence.name" .) .Values.rbac.nameOverride) (include "telepresence.namespace" .) }}
{{- end -}}

{{/*
RBAC rules required to create an intercept in a namespace; excludes any rules that are always cluster wide.
*/}}
{{- define "telepresence.clientRbacInterceptRules" -}}
- apiGroups:
  - ""
  resources: ["pods"]
  verbs: ["get", "list", "create", "watch", "delete"]
- apiGroups:
  - ""
  resources: ["services"]
  verbs: ["update"]
- apiGroups:
  - ""
  resources: ["pods/portforward"]
  verbs: ["create"]
- apiGroups:
  - "apps"
  resources: ["deployments", "replicasets", "statefulsets"]
  verbs: ["get", "list", "update", "patch"]
- apiGroups:
  - "getambassador.io"
  resources: ["hosts", "mappings"]
  verbs: ["*"]
- apiGroups:
  - ""
  resources: ["endpoints"]
  verbs: ["get", "list", "watch"]
{{- end }}

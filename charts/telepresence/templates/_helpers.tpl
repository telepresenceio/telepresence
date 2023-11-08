{{/*
Expand the name of the chart.
*/}}
{{- define "telepresence.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "traffic-manager.name" -}}
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

{{- /*
Traffic Manager Namespace
*/}}
{{- define "traffic-manager.namespace" -}}
{{- if .Values.isCI }}
{{- print "ambassador" }}
{{- else }}
{{- printf "%s" .Release.Namespace }}
{{- end }}
{{- end -}}

{{- /*
Create chart name and version as used by the chart label.
*/}}
{{- define "telepresence.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- /*
Common labels
*/}}
{{- define "telepresence.labels" -}}
{{ include "telepresence.selectorLabels" . }}
helm.sh/chart: {{ include "telepresence.chart" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- /* This value is intentionally undocumented -- it's used by the telepresence binary to determine ownership of the release */}}
{{- if .Values.createdBy }}
app.kubernetes.io/created-by: {{ .Values.createdBy }}
{{- else }}
app.kubernetes.io/created-by: {{ .Release.Service }}
{{- end }}
{{- end }}

{{- /*
Selector labels
*/}}
{{- define "telepresence.selectorLabels" -}}
app: traffic-manager
telepresence: manager
{{- end }}

{{- /*
Client RBAC name suffix
*/}}
{{- define "telepresence.clientRbacName" -}}
{{ printf "%s-%s" (default (include "telepresence.name" .) .Values.rbac.nameOverride) (include "traffic-manager.namespace" .) }}
{{- end -}}

{{- /*
RBAC rules required to create an intercept in a namespace; excludes any rules that are always cluster wide.
*/}}
{{- define "telepresence.clientRbacInterceptRules" -}}
- apiGroups: [""]
  resources: ["pods/log"]
  verbs: ["get"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["list", "get"]
- apiGroups: [""]
  resources: ["services"]
  verbs: ["list", "watch", "get"]
# Needed for the gather-traces command
- apiGroups: [""]
  resources: ["pods/portforward"]
  verbs: ["create"]
- apiGroups: ["apps"]
  resources: ["deployments", "replicasets", "statefulsets"]
  verbs: ["get", "watch", "list"]
- apiGroups: [""]
  resources: ["configmaps"]
  resourceNames: ["telepresence-agents"]
  verbs: ["get", "watch", "list"]
{{- if and .Values.clientRbac .Values.clientRbac.ruleExtras }}
{{ template "clientRbac-ruleExtras" . }}
{{- end }}
{{- end }}

{{/*
Kubernetes version
*/}}
{{- define "kube.version.major" }}
{{- $version := regexFind "^[0-9]+" .Capabilities.KubeVersion.Major -}}
{{- printf "%s" $version -}}
{{- end -}}

{{- define "kube.version.minor" }}
{{- $version := regexFind "^[0-9]+" .Capabilities.KubeVersion.Minor -}}
{{- printf "%s" $version -}}
{{- end -}}

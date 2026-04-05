{{/*
Expand the name of the chart.
*/}}
{{- define "jokowipe-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "jokowipe-agent.fullname" -}}
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
{{- define "jokowipe-agent.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "jokowipe-agent.labels" -}}
helm.sh/chart: {{ include "jokowipe-agent.chart" . }}
{{ include "jokowipe-agent.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "jokowipe-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "jokowipe-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "jokowipe-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "jokowipe-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Secret name for the API key.
*/}}
{{- define "jokowipe-agent.secretName" -}}
{{- if .Values.agent.existingSecret }}
{{- .Values.agent.existingSecret }}
{{- else }}
{{- include "jokowipe-agent.fullname" . }}
{{- end }}
{{- end }}

{{/*
Container image with tag fallback to appVersion.
*/}}
{{- define "jokowipe-agent.image" -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion }}
{{- printf "%s:%s" .Values.image.repository $tag }}
{{- end }}

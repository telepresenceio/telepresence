package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
)

func (i *Installer) generateCrashReport(sourceError error) {
	// TODO: Use the live endpoint
	//reportURL := "https://metriton.datawire.io/crash-report"
	reportURL := "https://metriton.datawire.io/beta/crash-report"

	report := &crashReportCreationRequest{
		Product:         "edgectl",
		Command:         "install",
		ProductVersion:  displayVersion,
		Error:           sourceError.Error(),
		AESVersion:      i.version,
		Address:         i.address,
		Hostname:        i.hostname,
		ClusterID:       i.clusterID,
		InstallID:       i.scout.installID,
		TraceID:         fmt.Sprintf("%v", i.scout.metadata["trace_id"]),
		ClusterInfo:     fmt.Sprintf("%v", i.scout.metadata["cluster_info"]),
		Managed:         fmt.Sprintf("%v", i.scout.metadata["managed"]),
		KubectlVersion:  i.k8sVersion.Client.GitVersion,
		KubectlPlatform: i.k8sVersion.Client.Platform,
		K8sVersion:      i.k8sVersion.Server.GitVersion,
		K8sPlatform:     i.k8sVersion.Server.Platform,
	}
	buf := new(bytes.Buffer)
	_ = json.NewEncoder(buf).Encode(report)
	resp, err := http.Post(reportURL, "application/json", buf)
	if err != nil {
		i.log.Printf("failed to initiate anonymous crash report due to error: %v", err.Error())
		return
	}
	defer resp.Body.Close()
	content, err := ioutil.ReadAll(resp.Body)
	if resp.StatusCode != 201 {
		i.log.Print("skipping anonymous crash report and log submission for this failure")
		i.log.Printf("%v: %q", resp.StatusCode, string(content))
		return
	}
	crashReport := crashReportCreationResponse{}
	err = json.Unmarshal(content, &crashReport)
	if err != nil {
		i.log.Printf("failed to generate anonymous crash report due to error: %v", err.Error())
		return
	}
	i.log.Printf("uploading anonymous crash report and logs under report ID: %v", crashReport.ReportId)
	i.Report("crash_report", ScoutMeta{"crash_report_id", crashReport.ReportId})
	i.uploadCrashReportData(crashReport, i.gatherCrashReportData())
}

func (i *Installer) gatherCrashReportData() []byte {
	buffer := bytes.NewBuffer([]byte{})

	buffer.WriteString("========== edgectl logs ==========\n")
	fileContent, err := ioutil.ReadFile(i.logName)
	if err != nil {
		i.log.Printf("failed to read log file %v: %v", i.logName, err.Error())
	}
	buffer.Write(fileContent)

	buffer.WriteString("\n========== kubectl describe ==========\n")
	describe, err := i.SilentCaptureKubectl("describe ambassador namespace", "", "-n", "ambassador", "describe", "all")
	if err != nil {
		i.log.Printf("failed to describe ambassador resources: %v", err.Error())
	}
	buffer.WriteString(describe)

	buffer.WriteString("\n========== kubectl logs ==========\n")
	ambassadorLogs, err := i.SilentCaptureKubectl("read ambassador logs", "", "-n", "ambassador", "logs", "deployments/ambassador", "--tail=1000")
	if err != nil {
		i.log.Printf("failed to read ambassador logs: %v", err.Error())
	}
	buffer.WriteString(ambassadorLogs)

	return buffer.Bytes()
}

func (i *Installer) uploadCrashReportData(crashReport crashReportCreationResponse, uploadContent []byte) {
	client := &http.Client{}
	req, err := http.NewRequest(crashReport.Method, crashReport.UploadURL, bytes.NewReader(uploadContent))
	if err != nil {
		i.log.Print(err.Error())
		return
	}

	res, err := client.Do(req)
	if err != nil {
		i.log.Print(err.Error())
		return
	}
	defer res.Body.Close()
	_, err = ioutil.ReadAll(res.Body)
	if err != nil {
		i.log.Print(err.Error())
		return
	}
}

// crashReportCreationRequest is used to initiate a crash report request
type crashReportCreationRequest struct {
	Product         string
	ProductVersion  string
	Command         string
	Error           string
	AESVersion      string
	Address         string
	Hostname        string
	ClusterID       string
	InstallID       string
	TraceID         string
	ClusterInfo     string
	Managed         string
	KubectlVersion  string
	KubectlPlatform string
	K8sVersion      string
	K8sPlatform     string
}

// crashReportCreationResponse is used to receive a crash report creation response
type crashReportCreationResponse struct {
	ReportId  string
	Method    string
	UploadURL string
}

package handlers

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const kubeSADir = "/var/run/secrets/kubernetes.io/serviceaccount"

// kubeServiceDeployments maps the monitored service names to the Deployments
// that run them when OpenAdmin itself runs inside Kubernetes/OKD, where there
// is no systemd, podman-compose or /root/docker-compose.yml.
var kubeServiceDeployments = map[string]string{
	"openpanel":       "openpanel",
	"admin":           "openadmin",
	"openpanel_mysql": "openpanel-mysql",
}

func runningInKube() bool {
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func kubeDeploymentFor(realName string) (string, bool) {
	d, ok := kubeServiceDeployments[realName]
	return d, ok
}

func kubeDo(method, path, contentType string, body []byte) ([]byte, int, error) {
	token, err := os.ReadFile(kubeSADir + "/token")
	if err != nil {
		return nil, 0, err
	}
	ns, err := os.ReadFile(kubeSADir + "/namespace")
	if err != nil {
		return nil, 0, err
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(kubeSADir + "/ca.crt"); err == nil {
		pool.AppendCertsFromPEM(ca)
	}
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	url := fmt.Sprintf("https://%s:%s/apis/apps/v1/namespaces/%s/deployments/%s",
		os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT"), strings.TrimSpace(string(ns)), path)
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return out, resp.StatusCode, nil
}

func kubeDeploymentReady(deployment string) *bool {
	out, code, err := kubeDo(http.MethodGet, deployment, "", nil)
	if err != nil || code != http.StatusOK {
		return nil
	}
	var d struct {
		Status struct {
			ReadyReplicas int `json:"readyReplicas"`
		} `json:"status"`
	}
	if json.Unmarshal(out, &d) != nil {
		return nil
	}
	up := d.Status.ReadyReplicas > 0
	return &up
}

func kubeControlService(realName, action string) (bool, string) {
	deployment, ok := kubeDeploymentFor(realName)
	if !ok {
		return false, "Service '" + realName + "' is not applicable on OKD: it is provided by the platform (OpenShift router, kubelet, ...)."
	}
	var patch string
	switch action {
	case "start":
		patch = `{"spec":{"replicas":1}}`
	case "stop":
		patch = `{"spec":{"replicas":0}}`
	case "restart":
		patch = fmt.Sprintf(`{"spec":{"replicas":1,"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, time.Now().UTC().Format(time.RFC3339))
	default:
		return false, "No command defined for the service action."
	}
	out, code, err := kubeDo(http.MethodPatch, deployment, "application/strategic-merge-patch+json", []byte(patch))
	if err != nil {
		return false, err.Error()
	}
	if code != http.StatusOK {
		return false, fmt.Sprintf("Kubernetes API returned %d: %s", code, strings.TrimSpace(string(out)))
	}
	return true, "Deployment " + deployment + " updated."
}

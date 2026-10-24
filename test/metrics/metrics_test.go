package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
	"github.com/shashankpatil-dev/sftpgate/internal/metrics"
)

const testConfig = `
version: 1
server:
  host_keys: [unused-offline]
storages:
  raw: mem://
accounts:
  - name: acme
    auth:
      public_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"]
    folders:
      - { path: /inbound, storage: raw, perms: [upload] }
`

// Before any event happens, the series an alert or panel needs must
// already exist at zero.
func TestPrepareCreatesSeriesAtZero(t *testing.T) {
	cfg, err := config.Parse([]byte(testConfig), config.Options{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	metrics.Prepare(cfg)

	want := map[string]map[string]string{
		"sftpgate_storage_errors_total": {"storage": "raw"},
		"sftpgate_config_reload_total":  {"result": "error"},
		"sftpgate_operations_total":     {"account": "acme", "folder": "/inbound", "op": "upload", "result": "denied"},
		"sftpgate_auth_total":           {"account": metrics.UnknownAccount, "result": "failed"},
		"sftpgate_connections_total":    {"result": "rejected_throttle"},
	}

	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for name, labels := range want {
		if !hasZeroSeries(families, name, labels) {
			t.Errorf("%s%v is missing or not zero", name, labels)
		}
	}
}

func hasZeroSeries(families []*dto.MetricFamily, name string, labels map[string]string) bool {
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			got := map[string]string{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = l.GetValue()
			}
			if matches(got, labels) && m.GetCounter().GetValue() == 0 {
				return true
			}
		}
	}
	return false
}

func matches(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

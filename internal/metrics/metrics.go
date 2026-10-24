// Package metrics defines the Prometheus metrics served on /metrics.
//
// Labels only ever hold names that come from the config (accounts, folders,
// storages). IPs, usernames typed by clients and file paths are never labels,
// so the number of series stays small.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/shashankpatil-dev/sftpgate/internal/config"
)

// UnknownAccount is the account label used when a login names no real account.
const UnknownAccount = "_unknown"

// All SFTPgate metrics.
var (
	Connections = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_connections_total",
		Help: "TCP connections, by whether they were accepted or rejected.",
	}, []string{"result"})

	Auth = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_auth_total",
		Help: "Login attempts.",
	}, []string{"account", "method", "result"})

	SessionsActive = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sftpgate_sessions_active",
		Help: "Open SFTP sessions.",
	}, []string{"account"})

	Operations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_operations_total",
		Help: "File operations.",
	}, []string{"account", "folder", "op", "result"})

	TransferBytes = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_transfer_bytes_total",
		Help: "Bytes uploaded (in) and downloaded (out).",
	}, []string{"account", "folder", "direction"})

	TransferDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sftpgate_transfer_duration_seconds",
		Help:    "How long uploads and downloads take.",
		Buckets: prometheus.ExponentialBuckets(0.05, 4, 8),
	}, []string{"direction"})

	StorageErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_storage_errors_total",
		Help: "Errors returned by a storage backend.",
	}, []string{"storage"})

	ThrottledIPs = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sftpgate_throttled_ips",
		Help: "IPs currently blocked after too many failed logins.",
	})

	SpoolFree = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "sftpgate_spool_free_bytes",
		Help: "Free space on the upload spool disk.",
	})

	ConfigReloads = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_config_reload_total",
		Help: "Config reloads, by result.",
	}, []string{"result"})

	Actions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "sftpgate_actions_total",
		Help: "Post-upload action runs. step=queue counts files that could not be queued.",
	}, []string{"step", "result"})

	ActionsQueue = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sftpgate_actions_queue_depth",
		Help: "Post-upload jobs waiting to run (pending) or given up on (failed).",
	}, []string{"state"})

	ConfigInfo = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "sftpgate_config_info",
		Help: "Always 1. The labels tell which config is running.",
	}, []string{"hash", "revision"})
)

// Operation names used in the "op" label.
var ops = []string{"upload", "download", "delete", "rename", "mkdir", "rmdir", "list"}

// Prepare creates, at zero, every series the config can produce.
//
// A counter that only appears when its first event happens loses that
// event: rate() and increase() have no earlier value to compare with, so
// the dashboard shows nothing and alerts don't fire. Call Prepare at
// startup and after every reload.
func Prepare(cfg *config.Config) {
	for _, result := range []string{"accepted", "rejected_ip", "rejected_throttle"} {
		Connections.WithLabelValues(result)
	}
	for _, result := range []string{"ok", "error"} {
		ConfigReloads.WithLabelValues(result)
	}
	Auth.WithLabelValues(UnknownAccount, "any", "failed")
	for _, step := range []string{"queue", "pgp_decrypt", "pgp_encrypt", "copy", "webhook", "job"} {
		Actions.WithLabelValues(step, "ok")
		Actions.WithLabelValues(step, "failed")
	}

	for name := range cfg.Storages {
		StorageErrors.WithLabelValues(name)
	}

	for _, a := range cfg.Accounts {
		SessionsActive.WithLabelValues(a.Name)
		for _, f := range a.Folders {
			for _, op := range ops {
				for _, result := range []string{"ok", "denied", "failed"} {
					Operations.WithLabelValues(a.Name, f.Path, op, result)
				}
			}
			TransferBytes.WithLabelValues(a.Name, f.Path, "in")
			TransferBytes.WithLabelValues(a.Name, f.Path, "out")
		}
	}
}

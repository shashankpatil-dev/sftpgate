package transfers

// EnabledHere reports whether this process should run scheduled transfers.
//
// Every replica reads the same config, so the choice is made with env vars:
//   - SFTPGATE_TRANSFERS=off turns transfers off here.
//   - SFTPGATE_TRANSFERS_ONLY_ON=<host> runs them only on the pod with that
//     host name, like "sftpgate-0" in a Kubernetes StatefulSet, so each
//     transfer runs once however many replicas there are.
func EnabledHere(getenv func(string) string, hostname string) bool {
	if getenv("SFTPGATE_TRANSFERS") == "off" {
		return false
	}
	only := getenv("SFTPGATE_TRANSFERS_ONLY_ON")
	return only == "" || only == hostname
}

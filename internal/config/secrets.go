package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gocloud.dev/runtimevar"

	// Secret stores, picked by the URL scheme of a ${secret:...} reference.
	_ "gocloud.dev/runtimevar/awsparamstore"
	_ "gocloud.dev/runtimevar/awssecretsmanager"
	_ "gocloud.dev/runtimevar/gcpsecretmanager"
)

// secretTimeout is how long reading one secret may take.
const secretTimeout = 10 * time.Second

// readSecret reads a value from a cloud secret store, for example:
//
//	gcpsecretmanager://projects/acme/secrets/razorpay-pw-hash
//	awssecretsmanager://razorpay-pw-hash?region=ap-south-1
//	awsparamstore://razorpay-pw-hash?region=ap-south-1
//
// The URL holds only the secret's name and region, never its value.
// Credentials come from the environment (workload identity, IAM role), the
// same way bucket access does. The value is read again on every reload, so a
// rotated secret is picked up on the next SIGHUP.
func readSecret(url string) (string, error) {
	// Return the value as text unless the URL asks for something else.
	if !strings.Contains(url, "decoder=") {
		sep := "?"
		if strings.Contains(url, "?") {
			sep = "&"
		}
		url += sep + "decoder=string"
	}

	ctx, cancel := context.WithTimeout(context.Background(), secretTimeout)
	defer cancel()

	v, err := runtimevar.OpenVariable(ctx, url)
	if err != nil {
		return "", fmt.Errorf("secret: %w", err)
	}
	defer func() { _ = v.Close() }()

	snap, err := v.Latest(ctx)
	if err != nil {
		return "", fmt.Errorf("secret: %w", err)
	}
	switch val := snap.Value.(type) {
	case string:
		return val, nil
	case []byte:
		return string(val), nil
	default:
		return "", errors.New("secret: value is not text")
	}
}

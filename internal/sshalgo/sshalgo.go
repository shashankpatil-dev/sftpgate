// Package sshalgo holds the SSH algorithms SFTPgate allows, for both the
// server and the outgoing connections of scheduled transfers.
//
// SHA-1 is never allowed. Peers that still need it must upgrade (decided in
// PRD open question 4). The x/crypto defaults still include some SHA-1
// options, so the lists are spelled out here.
package sshalgo

import "golang.org/x/crypto/ssh"

// Algorithm lists, strongest first.
var (
	KeyExchanges = []string{
		ssh.KeyExchangeMLKEM768X25519,
		ssh.KeyExchangeCurve25519,
		ssh.KeyExchangeECDHP256,
		ssh.KeyExchangeECDHP384,
		ssh.KeyExchangeECDHP521,
		ssh.KeyExchangeDH14SHA256,
	}

	MACs = []string{
		ssh.HMACSHA256ETM,
		ssh.HMACSHA512ETM,
		ssh.HMACSHA256,
		ssh.HMACSHA512,
	}

	// PublicKeys are the signature types accepted for keys: user keys on the
	// server, and the remote's host key on outgoing connections.
	PublicKeys = []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoSKED25519,
		ssh.KeyAlgoSKECDSA256,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSASHA512,
	}
)

// Config returns the key exchange and MAC settings.
func Config() ssh.Config {
	return ssh.Config{KeyExchanges: KeyExchanges, MACs: MACs}
}

// WithoutSHA1 makes an RSA key sign only with SHA-2. Other key types never
// use SHA-1, so they are returned as they are.
func WithoutSHA1(k ssh.Signer) ssh.Signer {
	as, ok := k.(ssh.AlgorithmSigner)
	if !ok || k.PublicKey().Type() != ssh.KeyAlgoRSA {
		return k
	}
	signer, err := ssh.NewSignerWithAlgorithms(as, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256})
	if err != nil {
		return k
	}
	return signer
}

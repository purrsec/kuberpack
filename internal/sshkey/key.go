package sshkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"

	"golang.org/x/crypto/ssh"
)

type Pair struct {
	PrivatePEM string
	Public     string
}

func Generate() (Pair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return Pair{}, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return Pair{}, err
	}
	return Pair{
		PrivatePEM: string(pem.EncodeToMemory(block)),
		Public:     string(ssh.MarshalAuthorizedKey(sshPub)),
	}, nil
}

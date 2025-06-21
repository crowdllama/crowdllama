package main

import (
	"crypto/rand"
	"os"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func main() {
	// Generate new private key
	privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		panic(err)
	}
	// Save to disk
	keyBytes, _ := crypto.MarshalPrivateKey(privKey)
	if err := os.WriteFile("dht.key", keyBytes, 0600); err != nil {
		panic(err)
	}
}

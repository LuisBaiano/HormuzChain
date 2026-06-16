package main

import (
	"fmt"
	"HormuzNet/internal/blockchain"
	"HormuzNet/internal/wallet"
)

func main() {
	defaultCompanies := []string{"Maersk", "MSC", "CMA_CGM", "Hapag_Lloyd", "ONE"}
	for _, name := range defaultCompanies {
		priv, pub := blockchain.DeterministicKey(name)
		addr := wallet.GetAddress(pub)
		fmt.Printf("Company: %s\n  Address: %s\n  PrivKey: %s\n  PubKey:  %s\n\n", name, addr, priv, pub)
	}
}

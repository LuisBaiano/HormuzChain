package blockchain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"HormuzNet/internal/models"
	"HormuzNet/internal/wallet"
)

// HashTx calculates the hash of a Transaction for verification/ID purposes.
func HashTx(tx models.Transaction) string {
	content := fmt.Sprintf("%s:%s:%s:%f:%s:%s:%s:%d:%d",
		tx.Type, tx.From, tx.To, tx.Amount, tx.VesselID, tx.OccurrenceID, tx.Payload, tx.Timestamp.UnixNano(), tx.Nonce)
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// HashBlock calculates the hash of a Block.
func HashBlock(b models.Block) string {
	txsHash := ""
	for _, tx := range b.Transactions {
		txsHash += HashTx(tx)
	}
	content := fmt.Sprintf("%d:%s:%s:%s:%s",
		b.Index, b.Timestamp.Format("2006-01-02 15:04:05"), txsHash, b.PrevHash, b.Validator)
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// SignBlock signs a block hash using the validator's private key.
func SignBlock(b *models.Block, privKeyHex string) error {
	b.Hash = HashBlock(*b)
	sig, err := wallet.Sign(privKeyHex, []byte(b.Hash))
	if err != nil {
		return err
	}
	b.Signature = sig
	return nil
}

// VerifyBlockSignature checks if the block signature is valid.
func VerifyBlockSignature(b models.Block, pubKeyHex string) bool {
	h := HashBlock(b)
	return wallet.Verify(pubKeyHex, []byte(h), b.Signature)
}

// VerifyTxSignature checks if the transaction signature is valid.
func VerifyTxSignature(tx models.Transaction) bool {
	if tx.Type == models.TxMint {
		return true
	}
	if tx.Type == models.TxTransfer && tx.OccurrenceID != "" {
		return wallet.Verify(tx.PublicKey, []byte(tx.OccurrenceID), tx.Signature)
	}
	h := HashTx(tx)
	return wallet.Verify(tx.PublicKey, []byte(h), tx.Signature)
}

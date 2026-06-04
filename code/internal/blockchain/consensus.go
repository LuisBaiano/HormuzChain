package blockchain

import (
	"HormuzNet/internal/models"
)

var Validators = []string{"B1", "B2", "B3", "B4"}

// GetProposer returns the broker ID that is allowed to propose block at the given index.
func GetProposer(index int) string {
	if len(Validators) == 0 {
		return ""
	}
	return Validators[index%len(Validators)]
}

// IsProposer returns true if the validatorID is the proposer for the block index.
func IsProposer(index int, validatorID string) bool {
	return GetProposer(index) == validatorID
}

// GetValidatorPubKey returns the public key hex for a validator broker using seed-based key generation.
func GetValidatorPubKey(validatorID string) string {
	_, pubKey := DeterministicKey(validatorID)
	return pubKey
}

// GetValidatorPrivKey returns the private key hex for a validator broker.
func GetValidatorPrivKey(validatorID string) string {
	privKey, _ := DeterministicKey(validatorID)
	return privKey
}

// VerifyBlockVotes verifies if the block has the required number of valid votes (at least 3 signatures out of 4).
func VerifyBlockVotes(b models.Block) bool {
	// A block must have a comma-separated list of validator signatures in b.Signature
	// Format: "B1:sig1,B2:sig2" etc.
	// In Genesis, we accept GENESIS validator with no signature check
	if b.Index == 0 {
		return b.Validator == "GENESIS"
	}

	parts := splitSemicolon(b.Signature)
	validVotes := 0
	votedValidators := make(map[string]bool)

	for _, part := range parts {
		subParts := splitColon(part)
		if len(subParts) != 2 {
			continue
		}
		valID, sig := subParts[0], subParts[1]
		
		// Ensure valID is in our validator list and has not voted twice
		isVal := false
		for _, v := range Validators {
			if v == valID {
				isVal = true
				break
			}
		}
		if !isVal || votedValidators[part] {
			continue
		}

		pubKey := GetValidatorPubKey(valID)
		if VerifyBlockSignature(models.Block{
			Index:        b.Index,
			Timestamp:    b.Timestamp,
			Transactions: b.Transactions,
			PrevHash:     b.PrevHash,
			Hash:         b.Hash,
			Validator:    b.Validator,
			Signature:    sig,
		}, pubKey) {
			validVotes++
			votedValidators[part] = true
		}
	}

	// For a 4-validator system, consensus requires >= 3 validators
	return validVotes >= 3
}

func splitSemicolon(s string) []string {
	var res []string
	cur := ""
	for _, c := range s {
		if c == ';' {
			res = append(res, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		res = append(res, cur)
	}
	return res
}

func splitColon(s string) []string {
	var res []string
	cur := ""
	for _, c := range s {
		if c == ':' {
			res = append(res, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		res = append(res, cur)
	}
	return res
}

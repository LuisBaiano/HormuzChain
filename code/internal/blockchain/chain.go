package blockchain

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"HormuzNet/internal/models"
	"HormuzNet/internal/wallet"
)

type Blockchain struct {
	mu        sync.RWMutex
	Blocks    []models.Block       `json:"blocks"`
	Mempool   []models.Transaction `json:"mempool"`
	FilePath  string               `json:"-"`
	
	// Real-time state derived from chain
	Balances       map[string]float64   `json:"-"`
	Vessels        map[string]string    `json:"-"` // vessel_id -> company_addr
	Companies      map[string]string    `json:"-"` // company_addr -> company_name
	CompanyPubKeys map[string]string    `json:"-"` // company_addr -> public_key
}

// NewBlockchain creates a new Blockchain instance, loading from filePath if exists.
func NewBlockchain(filePath string) (*Blockchain, error) {
	bc := &Blockchain{
		Blocks:         make([]models.Block, 0),
		Mempool:        make([]models.Transaction, 0),
		FilePath:       filePath,
		Balances:       make(map[string]float64),
		Vessels:        make(map[string]string),
		Companies:      make(map[string]string),
		CompanyPubKeys: make(map[string]string),
	}

	if err := bc.Load(); err != nil {
		return nil, err
	}

	return bc, nil
}

// DeterministicKey generates a deterministic ECDSA P-256 keypair from a string seed.
func DeterministicKey(seed string) (string, string) {
	hasher := sha256.Sum256([]byte(seed))
	d := new(big.Int).SetBytes(hasher[:])
	order := elliptic.P256().Params().N
	d.Mod(d, order)

	priv := &ecdsa.PrivateKey{
		D: d,
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
		},
	}
	priv.PublicKey.X, priv.PublicKey.Y = elliptic.P256().ScalarBaseMult(priv.D.Bytes())

	privHex := hex.EncodeToString(priv.D.Bytes())
	pubBytes := elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	pubHex := hex.EncodeToString(pubBytes)
	return privHex, pubHex
}

// Genesis initializes the blockchain with default companies.
func (bc *Blockchain) Genesis() {
	bc.Blocks = []models.Block{}
	
	// 5 Default Companies
	defaultCompanies := []string{"Maersk", "MSC", "CMA_CGM", "Hapag_Lloyd", "ONE"}
	var genesisTxs []models.Transaction

	for _, name := range defaultCompanies {
		_, pubHex := DeterministicKey(name)
		addr := wallet.GetAddress(pubHex)
		
		// 1. Mint 1000 ELIS to each
		mintTx := models.Transaction{
			ID:        fmt.Sprintf("genesis-mint-%s", name),
			Type:      models.TxMint,
			To:        addr,
			Amount:    1000.0,
			Payload:   fmt.Sprintf("Genesis Mint to %s", name),
			Timestamp: time.Unix(1717500000, 0), // fixed timestamp
		}
		// 2. Register Company on-chain
		regTx := models.Transaction{
			ID:        fmt.Sprintf("genesis-reg-%s", name),
			Type:      models.TxRegister,
			From:      addr,
			PublicKey: pubHex,
			Payload:   name,
			Timestamp: time.Unix(1717500000, 0),
		}
		
		genesisTxs = append(genesisTxs, mintTx, regTx)
	}

	genesisBlock := models.Block{
		Index:        0,
		Timestamp:    time.Unix(1717500000, 0),
		Transactions: genesisTxs,
		PrevHash:     "0000000000000000000000000000000000000000000000000000000000000000",
		Validator:    "GENESIS",
	}
	genesisBlock.Hash = HashBlock(genesisBlock)
	bc.Blocks = append(bc.Blocks, genesisBlock)
	bc.RebuildState()
}

// Load loads the blockchain from the JSON file.
func (bc *Blockchain) Load() error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	if _, err := os.Stat(bc.FilePath); os.IsNotExist(err) {
		bc.Genesis()
		bc.mu.Unlock() // unlock to call Save
		saveErr := bc.Save()
		bc.mu.Lock() // relock
		return saveErr
	}

	data, err := os.ReadFile(bc.FilePath)
	if err != nil {
		return err
	}

	type bcJSON struct {
		Blocks  []models.Block       `json:"blocks"`
		Mempool []models.Transaction `json:"mempool"`
	}

	var temp bcJSON
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	bc.Blocks = temp.Blocks
	bc.Mempool = temp.Mempool
	bc.RebuildState()
	return nil
}

// Save persists the blockchain to the JSON file.
func (bc *Blockchain) Save() error {
	bc.mu.RLock()
	type bcJSON struct {
		Blocks  []models.Block       `json:"blocks"`
		Mempool []models.Transaction `json:"mempool"`
	}
	temp := bcJSON{
		Blocks:  bc.Blocks,
		Mempool: bc.Mempool,
	}
	data, err := json.MarshalIndent(temp, "", "  ")
	bc.mu.RUnlock()
	if err != nil {
		return err
	}

	bc.mu.Lock()
	defer bc.mu.Unlock()
	return os.WriteFile(bc.FilePath, data, 0644)
}

// RebuildState recalculates balances, vessel registrations, and company registry.
func (bc *Blockchain) RebuildState() {
	bc.Balances = make(map[string]float64)
	bc.Vessels = make(map[string]string)
	bc.Companies = make(map[string]string)
	bc.CompanyPubKeys = make(map[string]string)

	for _, block := range bc.Blocks {
		for _, tx := range block.Transactions {
			bc.applyTx(tx)
		}
	}
}

func (bc *Blockchain) applyTx(tx models.Transaction) {
	switch tx.Type {
	case models.TxMint:
		bc.Balances[tx.To] += tx.Amount
	case models.TxRegister:
		bc.Companies[tx.From] = tx.Payload
		bc.CompanyPubKeys[tx.From] = tx.PublicKey
	case models.TxVesselReg:
		bc.Vessels[tx.VesselID] = tx.From
	case models.TxVesselLost:
		delete(bc.Vessels, tx.VesselID)
	case models.TxTransfer:
		bc.Balances[tx.From] -= tx.Amount
		bc.Balances[tx.To] += tx.Amount
	case models.TxDroneUsage:
		// Taxa de uso do drone: deduz da empresa, credita ao broker operador
		bc.Balances[tx.From] -= tx.Amount
		bc.Balances[tx.To] += tx.Amount
	case models.TxBrokerFee:
		// Taxa do broker: deduz da empresa, credita ao broker validador
		bc.Balances[tx.From] -= tx.Amount
		bc.Balances[tx.To] += tx.Amount
	case models.TxMissionLog:
		// Mission logs são informativos e imutáveis on-chain
	}
}

// GetBalance returns the balance of an address.
func (bc *Blockchain) GetBalance(addr string) float64 {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	return bc.Balances[addr]
}

// IsCompanyRegistered checks if an address belongs to a registered company.
func (bc *Blockchain) IsCompanyRegistered(addr string) bool {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	_, exists := bc.Companies[addr]
	return exists
}

// GetCompanyAddresses returns a slice of all registered company addresses.
func (bc *Blockchain) GetCompanyAddresses() []string {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	addrs := make([]string, 0, len(bc.Companies))
	for addr := range bc.Companies {
		addrs = append(addrs, addr)
	}
	return addrs
}


// GetCompanyPubKey returns the public key hex for a registered company address.
func (bc *Blockchain) GetCompanyPubKey(addr string) (string, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	pubKey, exists := bc.CompanyPubKeys[addr]
	return pubKey, exists
}

// GetVesselOwner returns the company address that owns the vessel.
func (bc *Blockchain) GetVesselOwner(vesselID string) (string, bool) {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	owner, exists := bc.Vessels[vesselID]
	return owner, exists
}

// AddTxToMempool validates and adds a transaction to the local mempool.
func (bc *Blockchain) AddTxToMempool(tx models.Transaction) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	// 1. Verify ID (hashing)
	expectedID := HashTx(tx)
	if tx.ID != expectedID {
		tx.ID = expectedID // ensure it is correct
	}

	// 2. Prevent duplicate txs in mempool or blocks
	for _, mTx := range bc.Mempool {
		if mTx.ID == tx.ID {
			return fmt.Errorf("transaction already in mempool")
		}
	}
	for _, block := range bc.Blocks {
		for _, bTx := range block.Transactions {
			if bTx.ID == tx.ID {
				return fmt.Errorf("transaction already in block")
			}
		}
	}

	// 3. Verify Signature (if not MINT)
	if tx.Type != models.TxMint {
		if !VerifyTxSignature(tx) {
			return fmt.Errorf("invalid transaction signature")
		}
		// Verify From matches the derived address of PublicKey
		derivedAddr := wallet.GetAddress(tx.PublicKey)
		if tx.From != derivedAddr {
			return fmt.Errorf("from address does not match public key")
		}
	}

	// 4. Verify Balance (if TRANSFER)
	if tx.Type == models.TxTransfer {
		// Calculate pending spent amount in mempool
		pendingSpent := 0.0
		for _, mTx := range bc.Mempool {
			if mTx.From == tx.From {
				pendingSpent += mTx.Amount
			}
		}
		currentBalance := bc.Balances[tx.From]
		if currentBalance < (tx.Amount + pendingSpent) {
			return fmt.Errorf("insufficient balance (balance: %.2f, pending spent: %.2f, tx: %.2f)",
				currentBalance, pendingSpent, tx.Amount)
		}
	}

	// 5. Verify Company registration (if TxRegister)
	if tx.Type == models.TxRegister {
		// Payload must be company name
		if tx.Payload == "" {
			return fmt.Errorf("company registration payload must contain company name")
		}
	}

	// 6. Verify Vessel registration owner
	if tx.Type == models.TxVesselReg {
		if tx.VesselID == "" {
			return fmt.Errorf("vessel registration must specify VesselID")
		}
		// check if company is registered
		if _, exists := bc.Companies[tx.From]; !exists {
			return fmt.Errorf("company must be registered before registering vessels")
		}
	}

	bc.Mempool = append(bc.Mempool, tx)
	return nil
}

// AddBlock validates and appends a block to the chain, clearing matching transactions from the mempool.
func (bc *Blockchain) AddBlock(b models.Block) error {
	bc.mu.Lock()
	defer bc.mu.Unlock()

	// 1. Validate index and prevHash
	latestBlock := bc.Blocks[len(bc.Blocks)-1]
	if b.Index != latestBlock.Index+1 {
		return fmt.Errorf("invalid block index: expected %d, got %d", latestBlock.Index+1, b.Index)
	}
	if b.PrevHash != latestBlock.Hash {
		return fmt.Errorf("invalid block prevHash: expected %s, got %s", latestBlock.Hash, b.PrevHash)
	}

	// 2. Validate hash
	expectedHash := HashBlock(b)
	if b.Hash != expectedHash {
		return fmt.Errorf("invalid block hash: expected %s, got %s", expectedHash, b.Hash)
	}

	// 3. Append and update state
	bc.Blocks = append(bc.Blocks, b)
	
	// Remove matching txs from Mempool
	newMempool := make([]models.Transaction, 0)
	for _, mTx := range bc.Mempool {
		found := false
		for _, bTx := range b.Transactions {
			if mTx.ID == bTx.ID {
				found = true
				break
			}
		}
		if !found {
			newMempool = append(newMempool, mTx)
		}
	}
	bc.Mempool = newMempool
	bc.RebuildState()

	return nil
}

// GetBlocks returns the slice of blocks (thread-safe read).
func (bc *Blockchain) GetBlocks() []models.Block {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	
	blocksCopy := make([]models.Block, len(bc.Blocks))
	copy(blocksCopy, bc.Blocks)
	return blocksCopy
}

// GetMempool returns the current mempool.
func (bc *Blockchain) GetMempool() []models.Transaction {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	
	mempoolCopy := make([]models.Transaction, len(bc.Mempool))
	copy(mempoolCopy, bc.Mempool)
	return mempoolCopy
}

// ClearMempool empties the mempool.
func (bc *Blockchain) ClearMempool() {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.Mempool = make([]models.Transaction, 0)
}

// ReplaceChain updates the chain with a new set of blocks.
func (bc *Blockchain) ReplaceChain(blocks []models.Block) {
	bc.mu.Lock()
	defer bc.mu.Unlock()
	bc.Blocks = blocks
	bc.RebuildState()
}

// GetAllTransactions retorna todas as transações confirmadas em todos os blocos.
func (bc *Blockchain) GetAllTransactions() []models.Transaction {
	bc.mu.RLock()
	defer bc.mu.RUnlock()
	var txs []models.Transaction
	for _, block := range bc.Blocks {
		txs = append(txs, block.Transactions...)
	}
	return txs
}

// LaudoInfo representa um laudo de missão consolidado (visão pública + detalhes privados).
type LaudoInfo struct {
	OccurrenceID  string  `json:"occurrence_id"`
	VesselID      string  `json:"vessel_id"`        // Sempre visível (público)
	DroneID       string  `json:"drone_id"`
	CompanyAddr   string  `json:"company_addr"`     // Endereço (público)
	EscortAmount  float64 `json:"escort_amount"`    // Taxa de serviço de escolta (público)
	DroneFee      float64 `json:"drone_fee"`        // Taxa de uso do drone (público)
	BrokerFee     float64 `json:"broker_fee"`       // Taxa do broker 5% (público)
	BrokerAddr    string  `json:"broker_addr"`      // Broker que processou (público)
	Payload       string  `json:"payload"`          // Detalhes — privado, só a empresa vê
	PayTxID       string  `json:"pay_tx_id"`        // ID da tx de pagamento
	LogTxID       string  `json:"log_tx_id"`        // ID da tx de laudo
	Timestamp     string  `json:"timestamp"`
}

// GetLaudos retorna todos os laudos de missão (TxMissionLog) com dados consolidados.
// companyAddr: se não vazio, revela os detalhes privados apenas para essa empresa.
func (bc *Blockchain) GetLaudos(companyAddr string) []LaudoInfo {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	// Indexa pagamentos por OccurrenceID
	type payData struct {
		amount    float64
		brokerFee float64
		brokerAddr string
		from      string
		txID      string
	}
	payments  := make(map[string]payData)
	feeTxs    := make(map[string]payData) // TxBrokerFee keyed by occurrence_id
	droneTxs  := make(map[string]payData) // TxDroneUsage keyed by occurrence_id

	var laudos []LaudoInfo
	var logTxs []models.Transaction

	for _, block := range bc.Blocks {
		for _, tx := range block.Transactions {
			switch tx.Type {
			case models.TxTransfer:
				if tx.OccurrenceID != "" {
					payments[tx.OccurrenceID] = payData{
						amount:     tx.Amount,
						brokerAddr: tx.To,
						from:       tx.From,
						txID:       tx.ID,
					}
				}
			case models.TxDroneUsage:
				if tx.OccurrenceID != "" {
					droneTxs[tx.OccurrenceID] = payData{
						amount:     tx.Amount,
						brokerAddr: tx.To,
						from:       tx.From,
						txID:       tx.ID,
					}
				}
			case models.TxBrokerFee:
				if tx.OccurrenceID != "" {
					feeTxs[tx.OccurrenceID] = payData{
						amount:     tx.Amount,
						brokerAddr: tx.To,
						from:       tx.From,
						txID:       tx.ID,
					}
				}
			case models.TxMissionLog:
				logTxs = append(logTxs, tx)
			}
		}
	}

	for _, tx := range logTxs {
		pay   := payments[tx.OccurrenceID]
		fee   := feeTxs[tx.OccurrenceID]
		drone := droneTxs[tx.OccurrenceID]

		// Payload privado: só a empresa solicitante vê
		payloadVis := "[CONFIDENCIAL — ACESSO RESTRITO AO CONTRATANTE]"
		isOwner := companyAddr != "" && companyAddr == pay.from
		if isOwner {
			payloadVis = tx.Payload
		}

		laudo := LaudoInfo{
			OccurrenceID: tx.OccurrenceID,
			VesselID:     tx.VesselID,
			DroneID:      tx.DroneID,
			CompanyAddr:  pay.from,
			EscortAmount: pay.amount,
			DroneFee:     drone.amount,
			BrokerFee:    fee.amount,
			BrokerAddr:   pay.brokerAddr,
			Payload:      payloadVis,
			PayTxID:      pay.txID,
			LogTxID:      tx.ID,
			Timestamp:    tx.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
		}
		laudos = append(laudos, laudo)
	}
	return laudos
}

// PaymentEntry representa uma entrada no histórico financeiro consolidado.
type PaymentEntry struct {
	TxID         string  `json:"tx_id"`
	Type         string  `json:"type"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	Amount       float64 `json:"amount"`
	OccurrenceID string  `json:"occurrence_id"`
	VesselID     string  `json:"vessel_id"`
	// Payload privado: só visível para a empresa envolvida
	Payload      string  `json:"payload"`
	Timestamp    string  `json:"timestamp"`
	BlockIndex   int     `json:"block_index"`
}

// GetPaymentHistory retorna o histórico de pagamentos consolidado.
// companyAddr: se não vazio, revela payloads privados apenas para essa empresa.
func (bc *Blockchain) GetPaymentHistory(companyAddr string) []PaymentEntry {
	bc.mu.RLock()
	defer bc.mu.RUnlock()

	var entries []PaymentEntry
	for _, block := range bc.Blocks {
		for _, tx := range block.Transactions {
			switch tx.Type {
			case models.TxTransfer, models.TxDroneUsage, models.TxBrokerFee, models.TxMint:
				payloadVis := "[PRIVADO]"
				isInvolved := companyAddr != "" &&
					(tx.From == companyAddr || tx.To == companyAddr)
				if isInvolved || tx.Type == models.TxMint {
					payloadVis = tx.Payload
				}
				entries = append(entries, PaymentEntry{
					TxID:         tx.ID,
					Type:         string(tx.Type),
					From:         tx.From,
					To:           tx.To,
					Amount:       tx.Amount,
					OccurrenceID: tx.OccurrenceID,
					VesselID:     tx.VesselID,
					Payload:      payloadVis,
					Timestamp:    tx.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
					BlockIndex:   block.Index,
				})
			}
		}
	}
	return entries
}

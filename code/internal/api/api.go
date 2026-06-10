package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"HormuzNet/internal/blockchain"
	"HormuzNet/internal/models"
	"HormuzNet/internal/wallet"
)

type APIServer struct {
	bc                 *blockchain.Blockchain
	broadcastTx        func(models.Transaction)
	getOpenOccurrences func() []models.Ocorrencia
	payOccurrence      func(occID string, from string, sig string) (string, error)
	getAllOccurrences  func() []models.Ocorrencia
	vesselKeepalive    func(string, float64, float64)
	getActiveVessels   func() map[string]models.Coordenada
}

func StartAPI(
	addr string,
	bc *blockchain.Blockchain,
	broadcastTx func(models.Transaction),
	getOpenOccurrences func() []models.Ocorrencia,
	payOccurrence func(occID string, from string, sig string) (string, error),
	getAllOccurrences func() []models.Ocorrencia,
	vesselKeepalive func(string, float64, float64),
	getActiveVessels func() map[string]models.Coordenada,
) {
	server := &APIServer{
		bc:                 bc,
		broadcastTx:        broadcastTx,
		getOpenOccurrences: getOpenOccurrences,
		payOccurrence:      payOccurrence,
		getAllOccurrences:  getAllOccurrences,
		vesselKeepalive:    vesselKeepalive,
		getActiveVessels:   getActiveVessels,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /company/register", server.handleRegister)
	mux.HandleFunc("GET /wallet/{addr}/balance", server.handleBalance)
	mux.HandleFunc("POST /wallet/pay", server.handlePay)
	mux.HandleFunc("POST /blockchain/tx", server.handleTx)
	mux.HandleFunc("POST /vessel/keepalive", server.handleVesselKeepalive)
	mux.HandleFunc("GET /vessels", server.handleVessels)
	mux.HandleFunc("GET /occurrences", server.handleOccurrences)
	mux.HandleFunc("GET /occurrences/{addr}", server.handleOccurrencesACL)
	mux.HandleFunc("GET /blockchain/blocks", server.handleBlocks)
	mux.HandleFunc("GET /blockchain/mempool", server.handleMempool)
	mux.HandleFunc("GET /blockchain/laudos", server.handleLaudos)
	mux.HandleFunc("GET /blockchain/payments", server.handlePayments)
	mux.HandleFunc("GET /blockchain/transactions", server.handleTransactions)

	go func() {
		if err := http.ListenAndServe(addr, mux); err != nil {
			fmt.Printf("[API ERROR] failed to start API on %s: %v\n", addr, err)
		}
	}()
}

func (s *APIServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var privHex, pubHex string
	var err error

	if req.PublicKey == "" {
		privHex, pubHex, err = wallet.GenerateKeyPair()
		if err != nil {
			http.Error(w, "failed to generate key pair", http.StatusInternalServerError)
			return
		}
	} else {
		pubHex = req.PublicKey
	}

	addr := wallet.GetAddress(pubHex)

	// Create TxRegister
	txReg := models.Transaction{
		Type:      models.TxRegister,
		From:      addr,
		PublicKey: pubHex,
		Payload:   req.Name,
		Timestamp: time.Now(),
	}
	txReg.ID = blockchain.HashTx(txReg)
	
	if req.PublicKey == "" {
		sig, err := wallet.Sign(privHex, []byte(txReg.ID))
		if err != nil {
			http.Error(w, "failed to sign transaction", http.StatusInternalServerError)
			return
		}
		txReg.Signature = sig
	}

	// Welcome Mint
	txMint := models.Transaction{
		Type:      models.TxMint,
		To:        addr,
		Amount:    1000.0,
		Payload:   fmt.Sprintf("Welcome Bonus for %s", req.Name),
		Timestamp: time.Now(),
	}
	txMint.ID = blockchain.HashTx(txMint)

	if err := s.bc.AddTxToMempool(txReg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.bc.AddTxToMempool(txMint); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.broadcastTx(txReg)
	s.broadcastTx(txMint)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"address":     addr,
		"public_key":  pubHex,
		"private_key": privHex,
		"name":        req.Name,
	})
}

func (s *APIServer) handleBalance(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")
	balance := s.bc.GetBalance(addr)

	// List vessels
	var ownedVessels []string
	s.bc.RebuildState() // ensure state is fresh
	
	// Thread-safe read by getting vessel owners
	for _, block := range s.bc.GetBlocks() {
		for _, tx := range block.Transactions {
			if tx.Type == models.TxVesselReg && tx.From == addr {
				// verify it hasn't been lost or transferred
				if owner, exists := s.bc.GetVesselOwner(tx.VesselID); exists && owner == addr {
					found := false
					for _, v := range ownedVessels {
						if v == tx.VesselID {
							found = true
							break
						}
					}
					if !found {
						ownedVessels = append(ownedVessels, tx.VesselID)
					}
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"address": addr,
		"balance": balance,
		"vessels": ownedVessels,
	})
}

func (s *APIServer) handlePay(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OccurrenceID string `json:"occurrence_id"`
		From         string `json:"from"`
		Signature    string `json:"signature"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	txID, err := s.payOccurrence(req.OccurrenceID, req.From, req.Signature)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"tx_id": txID})
}

func (s *APIServer) handleOccurrences(w http.ResponseWriter, r *http.Request) {
	occs := s.getOpenOccurrences()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(occs)
}

func (s *APIServer) handleOccurrencesACL(w http.ResponseWriter, r *http.Request) {
	addr := r.PathValue("addr")
	all := s.getAllOccurrences()

	var filtered []models.Ocorrencia
	for _, occ := range all {
		if occ.VesselID == "" {
			filtered = append(filtered, occ)
			continue
		}
		
		owner, exists := s.bc.GetVesselOwner(occ.VesselID)
		if exists && owner == addr {
			filtered = append(filtered, occ)
		} else {
			// Sanctified occurrence details for privacy
			sanitized := occ
			sanitized.Descricao = "ESCOLTA PRIVADA - DETALHES CONFIDENCIAIS"
			sanitized.VesselID = "HIDDEN"
			filtered = append(filtered, sanitized)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(filtered)
}

func (s *APIServer) handleTx(w http.ResponseWriter, r *http.Request) {
	var tx models.Transaction
	if err := json.NewDecoder(r.Body).Decode(&tx); err != nil {
		http.Error(w, "invalid transaction payload", http.StatusBadRequest)
		return
	}

	if err := s.bc.AddTxToMempool(tx); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.broadcastTx(tx)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"tx_id":  tx.ID,
		"status": "MEMPOOL",
	})
}

func (s *APIServer) handleVesselKeepalive(w http.ResponseWriter, r *http.Request) {
	var req struct {
		VesselID string  `json:"vessel_id"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if s.vesselKeepalive != nil {
		s.vesselKeepalive(req.VesselID, req.X, req.Y)
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func (s *APIServer) handleVessels(w http.ResponseWriter, r *http.Request) {
	var list map[string]models.Coordenada
	if s.getActiveVessels != nil {
		list = s.getActiveVessels()
	} else {
		list = make(map[string]models.Coordenada)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func (s *APIServer) handleBlocks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.bc.GetBlocks())
}

func (s *APIServer) handleMempool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.bc.GetMempool())
}

// handleTransactions — retorna todas as transações confirmadas em todos os blocos
func (s *APIServer) handleTransactions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.bc.GetAllTransactions())
}

// handleLaudos — retorna laudos de missão.
// Query param ?company=<addr> desbloqueia detalhes privados para essa empresa.
func (s *APIServer) handleLaudos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	companyAddr := r.URL.Query().Get("company")
	laudos := s.bc.GetLaudos(companyAddr)
	if laudos == nil {
		laudos = []blockchain.LaudoInfo{}
	}
	json.NewEncoder(w).Encode(laudos)
}

// handlePayments — retorna histórico financeiro consolidado.
// Query param ?company=<addr> desbloqueia payloads privados para essa empresa.
func (s *APIServer) handlePayments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	companyAddr := r.URL.Query().Get("company")
	payments := s.bc.GetPaymentHistory(companyAddr)
	if payments == nil {
		payments = []blockchain.PaymentEntry{}
	}
	json.NewEncoder(w).Encode(payments)
}

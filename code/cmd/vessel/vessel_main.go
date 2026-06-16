package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"HormuzNet/internal/blockchain"
	"HormuzNet/internal/models"
	"HormuzNet/internal/wallet"
)

type VesselKeepalive struct {
	VesselID string  `json:"vessel_id"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
}

func tryGet(apis []string, path string) (*http.Response, string, error) {
	client := http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for _, apiAddr := range apis {
		resp, err := client.Get(fmt.Sprintf("%s%s", apiAddr, path))
		if err == nil {
			return resp, apiAddr, nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func tryPost(apis []string, path string, contentType string, body []byte) (*http.Response, string, error) {
	client := http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for _, apiAddr := range apis {
		resp, err := client.Post(fmt.Sprintf("%s%s", apiAddr, path), contentType, bytes.NewBuffer(body))
		if err == nil {
			return resp, apiAddr, nil
		}
		lastErr = err
	}
	return nil, "", lastErr
}

func main() {
	vesselID := flag.String("id", "", "ID of the vessel (ex: vessel_01)")
	companyAddr := flag.String("company", "", "Company owner address")
	companyPriv := flag.String("key", "", "Company private key hex")
	brokerAPI := flag.String("broker-api", "http://localhost:7000", "Broker REST API address")
	posX := flag.Float64("x", 100, "Initial X position")
	posY := flag.Float64("y", 100, "Initial Y position")
	flag.Parse()

	// Fallback to environment variables
	if *vesselID == "" {
		*vesselID = os.Getenv("VESSEL_ID")
	}
	if *companyAddr == "" {
		*companyAddr = os.Getenv("COMPANY_ADDR")
	}
	if *companyPriv == "" {
		*companyPriv = os.Getenv("COMPANY_PRIV_KEY")
	}
	if os.Getenv("BROKER_API") != "" {
		*brokerAPI = os.Getenv("BROKER_API")
	}
	if os.Getenv("X") != "" {
		if x, err := strconv.ParseFloat(os.Getenv("X"), 64); err == nil {
			*posX = x
		}
	}
	if os.Getenv("Y") != "" {
		if y, err := strconv.ParseFloat(os.Getenv("Y"), 64); err == nil {
			*posY = y
		}
	}

	if *vesselID == "" || *companyAddr == "" || *companyPriv == "" {
		log.Fatalf("Missing required configuration (id, company, key). Set flags or env variables.")
	}

	log.Printf("Vessel %s owned by %s starting at (%.1f, %.1f)", *vesselID, *companyAddr, *posX, *posY)

	// Wait 3 seconds for brokers to wake up
	time.Sleep(3 * time.Second)

	// Split APIs by comma to handle fallback list
	apis := strings.Split(*brokerAPI, ",")

	// 1. Register Vessel on-chain
	registerVessel(*vesselID, *companyAddr, *companyPriv, apis)

	// 2. Loop keepalives & Auto-payments
	go autoPayLoop(*vesselID, *companyAddr, *companyPriv, apis)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Slow drift movement simulation
	vx := 0.2
	vy := 0.1

	for range ticker.C {
		*posX += vx
		*posY += vy
		
		// Ping-pong boundary collision
		if *posX < 50 || *posX > 950 {
			vx = -vx
		}
		if *posY < 50 || *posY > 950 {
			vy = -vy
		}

		sendKeepalive(*vesselID, *posX, *posY, apis)
	}
}

func autoPayLoop(vesselID, companyAddr, privKey string, apis []string) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		resp, activeAPI, err := tryGet(apis, "/occurrences")
		if err != nil {
			continue
		}

		var occs []models.Ocorrencia
		if err := json.NewDecoder(resp.Body).Decode(&occs); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		for _, occ := range occs {
			if occ.VesselID == vesselID && occ.Status == "AGUARDANDO_PAGAMENTO" {
				log.Printf("[AUTO-PAY %s] Escort required for occurrence %s. Cost: %.2f ELIS. Signing and paying...", vesselID, occ.ID, occ.CustoELIS)
				
				sig, err := wallet.Sign(privKey, []byte(occ.ID))
				if err != nil {
					log.Printf("[AUTO-PAY %s] Failed to sign payment signature: %v", vesselID, err)
					continue
				}

				payReq := struct {
					OccurrenceID string `json:"occurrence_id"`
					From         string `json:"from"`
					Signature    string `json:"signature"`
				}{
					OccurrenceID: occ.ID,
					From:         companyAddr,
					Signature:    sig,
				}

				payData, err := json.Marshal(payReq)
				if err != nil {
					continue
				}

				payResp, _, err := tryPost([]string{activeAPI}, "/wallet/pay", "application/json", payData)
				if err != nil {
					log.Printf("[AUTO-PAY %s] Payment request failed: %v", vesselID, err)
					continue
				}
				
				var payRes map[string]string
				_ = json.NewDecoder(payResp.Body).Decode(&payRes)
				payResp.Body.Close()

				log.Printf("[AUTO-PAY %s] Payment response status: %d, TxID: %s", vesselID, payResp.StatusCode, payRes["tx_id"])
				
				// Reconstruct transaction details for local logging to show complete tx in terminal
				mockTx := models.Transaction{
					ID:           payRes["tx_id"],
					Type:         models.TxTransfer,
					From:         companyAddr,
					To:           "Broker_Address", // Resolved by broker
					Amount:       occ.CustoELIS,
					OccurrenceID: occ.ID,
					VesselID:     vesselID,
					Payload:      fmt.Sprintf("Pagamento Escolta Ocorrencia %s", occ.ID),
					Signature:    sig,
				}
				if prettyData, err := json.MarshalIndent(mockTx, "", "  "); err == nil {
					log.Printf("\n======================================================\n[VESSEL %s] TRANSAÇÃO COMPLETA GERADA E ENVIADA:\n%s\n======================================================\n", vesselID, string(prettyData))
				}
			}
		}
	}
}


func registerVessel(vesselID, companyAddr, privKey string, apis []string) {
	compName := os.Getenv("COMPANY_NAME")
	if compName == "" {
		compName = "Maersk" // default fallback
	}
	_, pubKeyHex := blockchain.DeterministicKey(compName)

	tx := models.Transaction{
		Type:      models.TxVesselReg,
		From:      companyAddr,
		PublicKey: pubKeyHex,
		VesselID:  vesselID,
		Timestamp: time.Now(),
	}
	tx.ID = blockchain.HashTx(tx)
	sig, err := wallet.Sign(privKey, []byte(tx.ID))
	if err != nil {
		log.Printf("[VESSEL] Failed to sign vessel registration: %v", err)
		return
	}
	tx.Signature = sig

	data, err := json.Marshal(tx)
	if err != nil {
		return
	}

	resp, _, err := tryPost(apis, "/blockchain/tx", "application/json", data)
	if err != nil {
		log.Printf("[VESSEL] Failed to register vessel: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("[VESSEL] Registered on-chain. Response code: %d", resp.StatusCode)

	if prettyData, err := json.MarshalIndent(tx, "", "  "); err == nil {
		log.Printf("\n======================================================\n[VESSEL %s] TRANSAÇÃO DE REGISTRO COMPLETA:\n%s\n======================================================\n", vesselID, string(prettyData))
	}
}

func sendKeepalive(vesselID string, x, y float64, apis []string) {
	ka := VesselKeepalive{
		VesselID: vesselID,
		X:        x,
		Y:        y,
	}
	data, err := json.Marshal(ka)
	if err != nil {
		return
	}

	resp, _, err := tryPost(apis, "/vessel/keepalive", "application/json", data)
	if err != nil {
		return
	}
	resp.Body.Close()
}

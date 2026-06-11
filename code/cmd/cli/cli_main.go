package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"HormuzNet/internal/blockchain"
	"HormuzNet/internal/models"
	"HormuzNet/internal/wallet"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "balance":
		handleBalance()
	case "transfer":
		handleTransfer()
	case "register":
		handleRegister()
	case "vessel-reg":
		handleVesselReg()
	case "keys":
		handleKeys()
	default:
		fmt.Printf("Subcomando desconhecido: %s\n", subcommand)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Uso: cli <subcomando> [opções]")
	fmt.Println("Subcomandos:")
	fmt.Println("  balance     - Consulta o saldo de uma carteira")
	fmt.Println("  transfer    - Transfere ELIS entre empresas")
	fmt.Println("  register    - Registra uma nova empresa e ganha 1000 ELIS")
	fmt.Println("  vessel-reg  - Registra um navio associado a uma empresa")
	fmt.Println("  keys        - Obtém chaves e endereço determinísticos de uma empresa")
}

func handleKeys() {
	fs := flag.NewFlagSet("keys", flag.ExitOnError)
	companyFlag := fs.String("company", "", "Nome da empresa")
	fs.Parse(os.Args[2:])

	if *companyFlag == "" {
		log.Fatal("Erro: especifique -company")
	}

	priv, pub := blockchain.DeterministicKey(*companyFlag)
	addr := wallet.GetAddress(pub)
	fmt.Printf("%s %s\n", priv, addr)
}


func handleBalance() {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	addrFlag := fs.String("addr", "", "Endereço da carteira")
	companyFlag := fs.String("company", "", "Nome da empresa padrão (Maersk, MSC, etc.)")
	brokerFlag := fs.String("broker", "http://localhost:7000", "URL da REST API do broker")
	fs.Parse(os.Args[2:])

	addr := *addrFlag
	if addr == "" && *companyFlag != "" {
		_, pub := blockchain.DeterministicKey(*companyFlag)
		addr = wallet.GetAddress(pub)
	}

	if addr == "" {
		log.Fatal("Erro: especifique -addr ou -company")
	}

	url := fmt.Sprintf("%s/wallet/%s/balance", *brokerFlag, addr)
	resp, err := http.Get(url)
	if err != nil {
		log.Fatalf("Erro ao consultar saldo: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Erro do Broker (%d): %s", resp.StatusCode, string(body))
	}

	var res struct {
		Address string   `json:"address"`
		Balance float64  `json:"balance"`
		Vessels []string `json:"vessels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		log.Fatalf("Erro ao decodificar resposta: %v", err)
	}

	fmt.Printf("\n=== DETALHES DA CARTEIRA ===\n")
	if *companyFlag != "" {
		fmt.Printf("Empresa:    %s\n", *companyFlag)
	}
	fmt.Printf("Endereço:   %s\n", res.Address)
	fmt.Printf("Saldo:      %.2f ELIS\n", res.Balance)
	fmt.Printf("Navios:     %s\n", strings.Join(res.Vessels, ", "))
	fmt.Printf("============================\n\n")
}

func handleTransfer() {
	fs := flag.NewFlagSet("transfer", flag.ExitOnError)
	fromFlag := fs.String("from", "", "Nome da empresa ou chave privada hex do remetente")
	toFlag := fs.String("to", "", "Nome da empresa ou endereço da carteira do destinatário")
	amountFlag := fs.Float64("amount", 0.0, "Quantidade de ELIS para transferir")
	brokerFlag := fs.String("broker", "http://localhost:7000", "URL da REST API do broker")
	fs.Parse(os.Args[2:])

	if *fromFlag == "" || *toFlag == "" || *amountFlag <= 0 {
		log.Fatal("Erro: especifique -from, -to e -amount > 0")
	}

	var senderPriv, senderPub, senderAddr string
	// Tenta tratar -from como empresa ou chave privada
	if len(*fromFlag) == 64 {
		senderPriv = *fromFlag
		// Recupera chave pública a partir da privada para obter o endereço
		// Nota: o pacote wallet exporta funções de derivação ou podemos simplesmente usar o seed
		// Mas como chaves são determinísticas por nome, incentivamos usar o nome da empresa!
		log.Fatal("Erro: Para simplificar, informe o nome da empresa em -from (ex: Maersk, MSC, etc.)")
	} else {
		senderPriv, senderPub = blockchain.DeterministicKey(*fromFlag)
		senderAddr = wallet.GetAddress(senderPub)
	}

	var recipientAddr string
	if strings.HasPrefix(*toFlag, "0x") {
		recipientAddr = *toFlag
	} else {
		_, pub := blockchain.DeterministicKey(*toFlag)
		recipientAddr = wallet.GetAddress(pub)
	}

	tx := models.Transaction{
		Type:      models.TxTransfer,
		From:      senderAddr,
		To:        recipientAddr,
		Amount:    *amountFlag,
		PublicKey: senderPub,
		Payload:   fmt.Sprintf("Transferência via CLI por %s", *fromFlag),
		Timestamp: time.Now(),
	}

	tx.ID = blockchain.HashTx(tx)
	sig, err := wallet.Sign(senderPriv, []byte(tx.ID))
	if err != nil {
		log.Fatalf("Erro ao assinar transação: %v", err)
	}
	tx.Signature = sig

	data, err := json.Marshal(tx)
	if err != nil {
		log.Fatalf("Erro ao serializar transação: %v", err)
	}

	url := fmt.Sprintf("%s/blockchain/tx", *brokerFlag)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		log.Fatalf("Erro ao enviar transação: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Erro do Broker (%d): %s", resp.StatusCode, string(body))
	}

	fmt.Printf("\n✓ Transação enviada com sucesso!\nID: %s\nStatus: MEMPOOL (aguardando próximo bloco)\n\n", tx.ID)
}

func handleRegister() {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	nameFlag := fs.String("name", "", "Nome da nova empresa para registrar")
	brokerFlag := fs.String("broker", "http://localhost:7000", "URL da REST API do broker")
	fs.Parse(os.Args[2:])

	if *nameFlag == "" {
		log.Fatal("Erro: especifique -name")
	}

	payload := map[string]string{
		"name": *nameFlag,
	}
	data, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/company/register", *brokerFlag)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		log.Fatalf("Erro ao registrar empresa: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Erro do Broker (%d): %s", resp.StatusCode, string(body))
	}

	var res map[string]string
	if err := json.Unmarshal(body, &res); err != nil {
		log.Fatalf("Erro ao decodificar resposta: %v", err)
	}

	fmt.Printf("\n=== NOVA EMPRESA REGISTRADA ===\n")
	fmt.Printf("Nome:        %s\n", res["name"])
	fmt.Printf("Endereço:   %s\n", res["address"])
	fmt.Printf("Chave Priv: %s\n", res["private_key"])
	fmt.Printf("Chave Pub:  %s\n", res["public_key"])
	fmt.Printf("Bônus:      1000.00 ELIS (MINTED)\n")
	fmt.Printf("================================\n\n")
}

func handleVesselReg() {
	fs := flag.NewFlagSet("vessel-reg", flag.ExitOnError)
	companyFlag := fs.String("company", "", "Nome da empresa dona do navio")
	vesselFlag := fs.String("vessel", "", "Identificador único do navio")
	brokerFlag := fs.String("broker", "http://localhost:7000", "URL da REST API do broker")
	fs.Parse(os.Args[2:])

	if *companyFlag == "" || *vesselFlag == "" {
		log.Fatal("Erro: especifique -company e -vessel")
	}

	priv, pub := blockchain.DeterministicKey(*companyFlag)
	addr := wallet.GetAddress(pub)

	tx := models.Transaction{
		Type:      models.TxVesselReg,
		From:      addr,
		PublicKey: pub,
		VesselID:  *vesselFlag,
		Timestamp: time.Now(),
	}

	tx.ID = blockchain.HashTx(tx)
	sig, err := wallet.Sign(priv, []byte(tx.ID))
	if err != nil {
		log.Fatalf("Erro ao assinar transação: %v", err)
	}
	tx.Signature = sig

	data, err := json.Marshal(tx)
	if err != nil {
		log.Fatalf("Erro ao serializar transação: %v", err)
	}

	url := fmt.Sprintf("%s/blockchain/tx", *brokerFlag)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		log.Fatalf("Erro ao enviar registro do navio: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		log.Fatalf("Erro do Broker (%d): %s", resp.StatusCode, string(body))
	}

	fmt.Printf("\n✓ Registro do navio %s solicitado!\nID: %s\nStatus: MEMPOOL\n\n", *vesselFlag, tx.ID)
}

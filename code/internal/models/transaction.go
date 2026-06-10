package models

import "time"

type TxType string

const (
	TxMint       TxType = "MINT"        // Genesis/mint de tokens
	TxRegister   TxType = "REGISTER"    // Registro de empresa
	TxVesselReg  TxType = "VESSEL_REG"  // Registro de navio
	TxVesselLost TxType = "VESSEL_LOST" // Navio perdido
	TxTransfer   TxType = "TRANSFER"    // Taxa de serviço de escolta (coordenação)
	TxDroneUsage TxType = "DRONE_USAGE" // Taxa de uso do drone (hardware + deslocamento)
	TxMissionLog TxType = "MISSION_LOG" // Laudo da missão
	TxBrokerFee  TxType = "BROKER_FEE"  // Taxa do broker (5% sobre total)
)

const BrokerFeePercent = 0.05 // 5%

type Transaction struct {
	ID           string    `json:"id"`
	Type         TxType    `json:"type"`
	From         string    `json:"from,omitempty"`          // Endereço de origem
	To           string    `json:"to,omitempty"`            // Endereço de destino (broker/empresa)
	PublicKey    string    `json:"public_key,omitempty"`    // Chave pública hex para verificar assinatura
	Amount       float64   `json:"amount,omitempty"`        // Quantidade de ELIS
	CompanyAddr  string    `json:"company_addr,omitempty"`  // Empresa associada
	VesselID     string    `json:"vessel_id,omitempty"`     // Navio associado
	OccurrenceID string    `json:"occurrence_id,omitempty"` // Ocorrência
	DroneID      string    `json:"drone_id,omitempty"`      // Drone da missão
	Distance     float64   `json:"distance,omitempty"`      // Distância percorrida
	Payload      string    `json:"payload,omitempty"`       // Laudo/Metadados da transação
	Signature    string    `json:"signature,omitempty"`     // Assinatura digital ECDSA do remetente
	Timestamp    time.Time `json:"timestamp"`
	Nonce        int64     `json:"nonce"`
}

type Block struct {
	Index        int           `json:"index"`
	Timestamp    time.Time     `json:"timestamp"`
	Transactions []Transaction `json:"transactions"`
	PrevHash     string        `json:"prev_hash"`
	Hash         string        `json:"hash"`
	Validator    string        `json:"validator"`
	Signature    string        `json:"signature"`
}

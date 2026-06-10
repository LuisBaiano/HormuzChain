package pricing

import (
	"math"
	"HormuzNet/internal/models"
)

// ── Taxas de Serviço de Escolta (coordenação pelo broker) ─────────────────────
const (
	EscortBasePrice = 3.0  // ELIS: taxa base de coordenação/serviço
	EscortRate      = 0.03 // ELIS por unidade de distância (serviço)
)

// ── Taxas de Uso do Drone (hardware + deslocamento) ───────────────────────────
const (
	DroneBasePrice = 8.0  // ELIS: taxa fixa de acionamento do drone
	DroneRate      = 0.08 // ELIS por unidade de distância percorrida
)

// CustoDetalhado representa o custo de uma missão dividido em componentes.
type CustoDetalhado struct {
	EscortFee float64 // Taxa de serviço de escolta (coordenação)
	DroneFee  float64 // Taxa de uso do drone (hardware + distância)
	Total     float64 // EscortFee + DroneFee
	Distancia float64 // Distância euclidiana real entre os pontos
}

// CalcularCustoDetalhado retorna o custo discriminado por componente.
func CalcularCustoDetalhado(p1, p2 models.Coordenada) CustoDetalhado {
	squaredDist := p1.Distancia(p2)
	dist := math.Sqrt(squaredDist)

	escort := EscortBasePrice + dist*EscortRate
	drone := DroneBasePrice + dist*DroneRate
	return CustoDetalhado{
		EscortFee: escort,
		DroneFee:  drone,
		Total:     escort + drone,
		Distancia: dist,
	}
}

// CalcularCusto mantém compatibilidade com o código existente (retorna total).
func CalcularCusto(p1, p2 models.Coordenada) float64 {
	c := CalcularCustoDetalhado(p1, p2)
	return c.Total
}

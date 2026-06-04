package pricing

import (
	"math"
	"HormuzNet/internal/models"
)

const (
	BasePrice = 5.0
	Rate      = 0.05
)

// CalcularCusto calculates the price of an escort mission based on Euclidean distance.
func CalcularCusto(p1, p2 models.Coordenada) float64 {
	squaredDist := p1.Distancia(p2)
	dist := math.Sqrt(squaredDist)
	return BasePrice + dist*Rate
}

/*
Este arquivo implementa os Sensores Simulados do HormuzNet.
Ele emula os dispositivos de detecção física (radares, sonares, boias inteligentes, câmeras e meteorológicos).
Cada sensor gera dados e níveis de criticidade de acordo com sua tipagem.
Se houver um navio em seu raio de detecção, o sensor associa o VesselID à leitura.
*/
package main

import (
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net"
	"net/http"
	"sync"
	"time"

	"HormuzNet/internal/models"
)

var (
	vesselsMu  sync.RWMutex
	vesselsMap = make(map[string]models.Coordenada)
)

func loopUpdateVessels(brokerAPI string) {
	client := http.Client{Timeout: 1 * time.Second}
	for {
		resp, err := client.Get(brokerAPI + "/vessels")
		if err == nil {
			var temp map[string]models.Coordenada
			if err := json.NewDecoder(resp.Body).Decode(&temp); err == nil {
				vesselsMu.Lock()
				vesselsMap = temp
				vesselsMu.Unlock()
			}
			resp.Body.Close()
		}
		time.Sleep(2 * time.Second)
	}
}

func main() {
	id := flag.String("id", "sensor_01", "ID do sensor")
	tipo := flag.String("tipo", "radar", "Tipo do sensor (radar, sonar, boia, visual, meteo)")
	setor := flag.String("setor", "Setor_Norte", "ID do setor")
	broker := flag.String("broker", "224.1.2.3:9876", "Endereço Multicast UDP")
	brokerAPI := flag.String("broker-api", "http://localhost:7000", "Endereço API HTTP do broker")
	intervalo := flag.Int("intervalo", 2000, "Intervalo entre leituras (ms)")
	posX := flag.Float64("x", 0, "Posição X inicial")
	posY := flag.Float64("y", 0, "Posição Y inicial")
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	// Inicia thread para escutar posições de navios
	go loopUpdateVessels(*brokerAPI)

	// Resolve endereço UDP do broker
	addr, err := net.ResolveUDPAddr("udp", *broker)
	if err != nil {
		log.Fatalf("Erro ao resolver endereço: %v", err)
	}

	// Abre conexão UDP
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatalf("Erro ao conectar UDP: %v", err)
	}
	defer conn.Close()

	log.Printf("Sensor %s [%s] iniciado. Enviando para %s (API: %s) a cada %dms", *id, *tipo, *broker, *brokerAPI, *intervalo)

	ticker := time.NewTicker(time.Duration(*intervalo) * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		leitura := gerarLeitura(*id, *tipo, *setor, *posX, *posY)
		dados, err := json.Marshal(leitura)
		if err != nil {
			continue
		}
		
		_, err = conn.Write(dados)
		if err != nil {
			log.Printf("Erro ao enviar dados: %v", err)
		} else {
			log.Printf("[DADO ENVIADO] Sensor %s envia leitura tipo=%s, crit=%s, valor=%.2f %s, vessel=%s",
				leitura.SensorID, leitura.Tipo, leitura.Criticidade.String(), leitura.Valor, leitura.Unidade, leitura.VesselID)
		}
	}
}

func gerarLeitura(id, tipo, setor string, x, y float64) models.LeituraSensor {
	sensorPos := models.Coordenada{X: x, Y: y}

	// Verifica se há navio no raio de detecção (raio = 150.0, raio^2 = 22500)
	var closestVessel string
	var minDist float64
	hasVessel := false

	vesselsMu.RLock()
	for vID, vPos := range vesselsMap {
		dist := sensorPos.Distancia(vPos)
		if dist < 22500 {
			if !hasVessel || dist < minDist {
				closestVessel = vID
				minDist = dist
				hasVessel = true
			}
		}
	}
	vesselsMu.RUnlock()

	var valor float64
	var unidade string
	crit := models.CriticidadeNula

	// Se hover um navio por perto, a chance de gerar um alerta de segurança aumenta!
	if hasVessel {
		valor = rand.Float64() * 100
		unidade = "confiança"
		
		// 15% de chance de ocorrência de segurança crítica no navio
		if rand.Float64() < 0.15 {
			crit = models.CriticidadeAlta
		} else {
			crit = models.CriticidadeNula
		}

		return models.LeituraSensor{
			SensorID:    id,
			SetorID:     setor,
			Tipo:        tipo,
			Posicao:     sensorPos,
			Valor:       valor,
			Unidade:     unidade,
			Criticidade: crit,
			VesselID:    closestVessel,
			Timestamp:   time.Now(),
		}
	}

	// Se não há navio, mantém a lógica de ocorrências ambientais normais com 45% de chance
	if rand.Float64() > 0.45 {
		return models.LeituraSensor{
			SensorID:    id,
			SetorID:     setor,
			Tipo:        tipo,
			Posicao:     sensorPos,
			Valor:       0,
			Unidade:     "-",
			Criticidade: crit,
			Timestamp:   time.Now(),
		}
	}

	switch tipo {
	case "radar":
		valor = rand.Float64() * 100
		unidade = "objetos"
		if valor > 75 {
			crit = models.CriticidadeAlta
		} else {
			crit = models.CriticidadeBaixa
		}
	case "sonar":
		valor = rand.Float64() * 150
		unidade = "dB"
		if valor > 100 {
			crit = models.CriticidadeAlta
		} else {
			crit = models.CriticidadeBaixa
		}
	case "boia":
		valor = rand.Float64() * 12
		unidade = "m"
		if valor > 7 {
			crit = models.CriticidadeAlta
		} else {
			crit = models.CriticidadeBaixa
		}
	case "visual":
		valor = rand.Float64()
		unidade = "confiança"
		if valor > 0.85 {
			crit = models.CriticidadeAlta
		} else {
			crit = models.CriticidadeBaixa
		}
	case "meteo":
		valor = rand.Float64()*60 - 10
		unidade = "°C"
		if valor > 45 || valor < -5 {
			crit = models.CriticidadeAlta
		} else {
			crit = models.CriticidadeBaixa
		}
	default:
		valor = rand.Float64() * 100
		unidade = "un"
		crit = models.CriticidadeBaixa
	}

	if rand.Float64() < 0.07 {
		crit = models.CriticidadeAlta
	}

	return models.LeituraSensor{
		SensorID:    id,
		SetorID:     setor,
		Tipo:        tipo,
		Posicao:     sensorPos,
		Valor:       valor,
		Unidade:     unidade,
		Criticidade: crit,
		Timestamp:   time.Now(),
	}
}

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
	"os"
	"strings"
	"sync"
	"time"

	"HormuzNet/internal/models"
)

var (
	vesselsMu  sync.RWMutex
	vesselsMap = make(map[string]models.Coordenada)
)

func loopUpdateVessels(brokerAPIs string) {
	apis := strings.Split(brokerAPIs, ",")
	client := http.Client{Timeout: 1 * time.Second}
	for {
		success := false
		for _, apiAddr := range apis {
			apiAddr = strings.TrimSpace(apiAddr)
			if apiAddr == "" {
				continue
			}
			resp, err := client.Get(apiAddr + "/vessels")
			if err == nil {
				var temp map[string]models.Coordenada
				if err := json.NewDecoder(resp.Body).Decode(&temp); err == nil {
					vesselsMu.Lock()
					vesselsMap = temp
					vesselsMu.Unlock()
					success = true
				}
				resp.Body.Close()
			}
			if success {
				break
			}
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

	if envBrokerAPI := os.Getenv("BROKER_API"); envBrokerAPI != "" {
		*brokerAPI = envBrokerAPI
	}

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
		if leitura.Criticidade == models.CriticidadeNula {
			continue // Não envia nem loga se não houver navio para cobrar
		}
		
		dados, err := json.Marshal(leitura)
		if err != nil {
			continue
		}
		
		_, err = conn.Write(dados)
		if err != nil {
			log.Printf("Erro ao enviar dados: %v", err)
		} else {
			log.Printf("[ALERTA ENVIADO] Sensor %s detectou navio %s! Ocorrência crítica gerada para cobrança.",
				leitura.SensorID, leitura.VesselID)
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

	// Se houver um navio por perto, gera sempre um alerta crítico de segurança para que ocorra a cobrança
	if hasVessel {
		return models.LeituraSensor{
			SensorID:    id,
			SetorID:     setor,
			Tipo:        tipo,
			Posicao:     sensorPos,
			Valor:       rand.Float64() * 100,
			Unidade:     "confiança",
			Criticidade: models.CriticidadeAlta, // Sempre alta para garantir cobrança
			VesselID:    closestVessel,
			Timestamp:   time.Now(),
		}
	}

	// Caso contrário, retorna criticidade nula (será ignorada/não enviada)
	return models.LeituraSensor{
		SensorID:    id,
		SetorID:     setor,
		Tipo:        tipo,
		Posicao:     sensorPos,
		Valor:       0,
		Unidade:     "-",
		Criticidade: models.CriticidadeNula,
		Timestamp:   time.Now(),
	}
}

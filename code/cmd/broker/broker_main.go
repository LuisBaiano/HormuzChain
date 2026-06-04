/*
Este arquivo implementa o Broker de Setor do HormuzNet estendido para o HormuzChain.
Ele gerencia as ocorrências do seu respectivo setor geográfico do Estreito de Ormuz.

Responsabilidades principais:
  - Escutar leituras de sensores via UDP Multicast (grupo 224.1.2.3:9876)
  - Aceitar conexões TCP de Drones locais e outros Brokers da malha
  - Manter uma fila de prioridades local com ordenação por Relógio de Lamport
  - Sincronizar o estado global de Drones e Ocorrências via protocolo Gossip P2P
  - Disparar Heartbeats periódicos para detectar falhas de vizinhos
  - Assumir setores de Brokers caídos usando lógica de Ring Failover
  - Blockchain: gerenciar mempool de transações, persistência em chain_<id>.json
  - Consenso PoA: propor blocos via Round-Robin a cada 10s se mempool > 0, votar e comitar
  - REST API: expor carteiras, registros, cotações e pagamentos (porta TCP + 1000)
  - Despacho: aguardar pagamento on-chain para navios, direcionando o drone à coordenada atual do navio
*/
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"HormuzNet/internal/api"
	"HormuzNet/internal/blockchain"
	"HormuzNet/internal/fila"
	"HormuzNet/internal/models"
	"HormuzNet/internal/pricing"
	"HormuzNet/internal/wallet"
)

// ── Constantes ────────────────────────────────────────────────────────────────

const (
	heartbeatInterval  = 5 * time.Second
	heartbeatTimeout   = 15 * time.Second
	envelhecerInterval = 10 * time.Second
	despachoInterval   = 500 * time.Millisecond
	ociosidadeTimeout  = 60 * time.Second
	consensusInterval  = 10 * time.Second
)

// ── Estrutura do broker ───────────────────────────────────────────────────────

type ConsensusState struct {
	mu          sync.Mutex
	votedBlocks map[string]bool                 // blockHash -> true
	votes       map[string][]string             // blockHash -> list of "ValidatorID:Signature"
	proposals   map[string]models.Block         // blockHash -> Block
}

type Broker struct {
	id       string
	setorID  string
	portaUDP string
	portaTCP string

	liderID       string
	liderIDMu     sync.RWMutex
	statusEleicao string // "ESTAVEL" ou "EM_ELEICAO"
	okRecebido    bool
	eleicaoMu     sync.Mutex

	lamport   int
	lamportMu sync.Mutex

	fila *fila.FilaPrioridade

	// Drones conectados localmente: drone_id → conn
	dronesLocaisMu sync.RWMutex
	dronesLocais   map[string]net.Conn

	// Lista GLOBAL de drones (todos os brokers sincronizam)
	dronesMu sync.RWMutex
	drones   map[string]models.InfoDrone

	// Brokers vizinhos: broker_id → conn (ou IP:Port -> conn)
	vizinhosMu sync.RWMutex
	vizinhos   map[string]net.Conn

	heartbeatMu sync.Mutex
	ultimoHB    map[string]time.Time

	atendidosMu sync.Mutex
	atendidos   map[string]bool

	ocorrenciasMu sync.RWMutex
	ocorrencias   map[string]models.Ocorrencia

	peersConhecidosMu sync.RWMutex
	peersConhecidos   map[string]bool // "IP:PORT" -> true

	setoresConhecidosMu sync.RWMutex
	setoresConhecidos   map[string]string // BrokerID -> SetorID

	brokersMortosMu sync.RWMutex
	brokersMortos   map[string]bool // BrokerID -> true (mortos)

	// HormuzChain Blockchain and Vessel state
	blockchain      *blockchain.Blockchain
	blockchainMu    sync.RWMutex
	consensus       ConsensusState
	
	vesselsMu       sync.RWMutex
	vesselPositions map[string]models.Coordenada
	vesselLastSeen  map[string]time.Time

	logger *log.Logger
}

func novoBroker(id, setorID, portaUDP, portaTCP string) *Broker {
	chainPath := fmt.Sprintf("chain_%s.json", id)
	bc, err := blockchain.NewBlockchain(chainPath)
	if err != nil {
		log.Fatalf("Erro ao inicializar blockchain local: %v", err)
	}

	return &Broker{
		id:                id,
		setorID:           setorID,
		portaUDP:          portaUDP,
		portaTCP:          portaTCP,
		liderID:           "B1", // Em 2x2, B1 é o líder de descoberta inicial
		statusEleicao:     "ESTAVEL",
		lamport:           0,
		fila:              fila.Nova(),
		dronesLocais:      make(map[string]net.Conn),
		drones:            make(map[string]models.InfoDrone),
		vizinhos:          make(map[string]net.Conn),
		ultimoHB:          make(map[string]time.Time),
		atendidos:         make(map[string]bool),
		ocorrencias:       make(map[string]models.Ocorrencia),
		peersConhecidos:   make(map[string]bool),
		setoresConhecidos: make(map[string]string),
		brokersMortos:     make(map[string]bool),
		
		blockchain:        bc,
		consensus: ConsensusState{
			votedBlocks: make(map[string]bool),
			votes:       make(map[string][]string),
			proposals:   make(map[string]models.Block),
		},
		vesselPositions:   make(map[string]models.Coordenada),
		vesselLastSeen:    make(map[string]time.Time),
		
		logger:            log.New(os.Stdout, fmt.Sprintf("[BROKER:%s] ", id), log.LstdFlags),
	}
}

// Relógio de Lamport
func (b *Broker) tick() int {
	b.lamportMu.Lock()
	defer b.lamportMu.Unlock()
	b.lamport++
	return b.lamport
}

func (b *Broker) syncLamport(recebido int) {
	b.lamportMu.Lock()
	defer b.lamportMu.Unlock()
	if recebido > b.lamport {
		b.lamport = recebido
	}
	b.lamport++
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	id := flag.String("id", "", "ID único do broker (ex: B1)")
	setor := flag.String("setor", "", "ID do setor (ex: Setor_Norte)")
	udp := flag.String("udp", "224.1.2.3:9876", "Endereço Multicast UDP para sensores")
	tcp := flag.String("tcp", "0.0.0.0:6000", "Porta TCP para drones e brokers")
	vizStr := flag.String("vizinhos", "", "Endereços TCP de brokers vizinhos (vírgula)")
	lider := flag.String("lider", "", "IP:PORT do Broker Lider para descoberta (se vazio, assume como lider)")
	flag.Parse()

	if *id == "" || *setor == "" {
		fmt.Fprintln(os.Stderr, "Uso: broker -id B1 -setor Setor_Norte [-udp :8080] [-tcp :6000] [-vizinhos IP:6000,IP:6000] [-lider IP:6000]")
		os.Exit(1)
	}

	b := novoBroker(*id, *setor, *udp, *tcp)
	if *lider == "" {
		b.liderID = *id
	}
	b.logger.Printf("Iniciando HormuzChain — setor=%s UDP=%s TCP=%s", *setor, *udp, *tcp)

	// Calcula porta da API REST (TCP + 1000)
	_, tcpPort, _ := net.SplitHostPort(*tcp)
	var tcpPortNum int
	fmt.Sscanf(tcpPort, "%d", &tcpPortNum)
	apiAddr := fmt.Sprintf("0.0.0.0:%d", tcpPortNum+1000)

	// Callbacks da API REST
	broadcastTx := func(tx models.Transaction) {
		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgTxBroadcast,
			BrokerID:    b.id,
			Transaction: &tx,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		})
	}
	getOpenOccs := func() []models.Ocorrencia {
		b.ocorrenciasMu.RLock()
		defer b.ocorrenciasMu.RUnlock()
		var res []models.Ocorrencia
		for _, occ := range b.ocorrencias {
			if occ.Status == "AGUARDANDO_PAGAMENTO" || occ.Status == "ABERTA" {
				res = append(res, occ)
			}
		}
		return res
	}
	payOcc := func(occID string, from string, sig string) (string, error) {
		return b.pagarOcorrencia(occID, from, sig)
	}
	getAllOccs := func() []models.Ocorrencia {
		b.ocorrenciasMu.RLock()
		defer b.ocorrenciasMu.RUnlock()
		var res []models.Ocorrencia
		for _, occ := range b.ocorrencias {
			res = append(res, occ)
		}
		return res
	}
	vesselKeepalive := func(vesselID string, x float64, y float64) {
		b.vesselsMu.Lock()
		b.vesselPositions[vesselID] = models.Coordenada{X: x, Y: y}
		b.vesselLastSeen[vesselID] = time.Now()
		b.vesselsMu.Unlock()

		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgVesselRegistered,
			BrokerID:    b.id,
			VesselID:    vesselID,
			Payload:     fmt.Sprintf("%f,%f", x, y),
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		})
	}
	getActiveVessels := func() map[string]models.Coordenada {
		b.vesselsMu.RLock()
		defer b.vesselsMu.RUnlock()
		res := make(map[string]models.Coordenada)
		for k, v := range b.vesselPositions {
			if time.Since(b.vesselLastSeen[k]) < 15*time.Second {
				res[k] = v
			}
		}
		return res
	}

	api.StartAPI(apiAddr, b.blockchain, broadcastTx, getOpenOccs, payOcc, getAllOccs, vesselKeepalive, getActiveVessels)
	b.logger.Printf("REST API iniciada em %s", apiAddr)

	go b.escutarTCP()
	go b.escutarUDP()
	
	if *lider != "" {
		b.logger.Printf("Modo Seguidor: Conectando ao Líder de Descoberta em %s", *lider)
		go b.conectarLider(*lider)
	} else if *vizStr != "" {
		go b.conectarVizinhos(*vizStr)
	} else {
		b.logger.Printf("Modo Líder: Aguardando brokers se conectarem para descoberta.")
	}

	go b.loopHeartbeat()
	go b.loopDetectarFalhas()
	go b.loopEnvelhecerFila()
	go b.loopDespachar()
	go b.loopVerificarOciosidade()
	go b.loopConsensus()
	go b.loopReposicaoPeriodica()


	// Blockchain Sync Inicial
	go func() {
		time.Sleep(5 * time.Second)
		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgChainSync,
			BrokerID:    b.id,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		})
	}()

	select {}
}

// ── UDP — sensores ────────────────────────────────────────────────────────────

func (b *Broker) escutarUDP() {
	addr, err := net.ResolveUDPAddr("udp", b.portaUDP)
	if err != nil {
		b.logger.Fatalf("UDP addr: %v", err)
	}
	conn, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		b.logger.Fatalf("UDP multicast listen: %v", err)
	}
	defer conn.Close()
	b.logger.Printf("Escutando sensores em Multicast UDP %s", b.portaUDP)

	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		go b.processarLeitura(buf[:n])
	}
}

func (b *Broker) responsavelPorSetor(setorDaLeitura string) bool {
	if setorDaLeitura == b.setorID {
		return true
	}

	donoOriginal := ""
	b.setoresConhecidosMu.RLock()
	for brokerID, setor := range b.setoresConhecidos {
		if setor == setorDaLeitura {
			donoOriginal = brokerID
			break
		}
	}
	b.setoresConhecidosMu.RUnlock()

	if donoOriginal == "" {
		return false
	}

	b.brokersMortosMu.RLock()
	donoMorto := b.brokersMortos[donoOriginal]
	b.brokersMortosMu.RUnlock()

	if !donoMorto {
		return false
	}

	b.setoresConhecidosMu.RLock()
	var todos []string
	for brokerID := range b.setoresConhecidos {
		todos = append(todos, brokerID)
	}
	b.setoresConhecidosMu.RUnlock()
	
	encontrouEu := false
	for _, br := range todos {
		if br == b.id { encontrouEu = true; break }
	}
	if !encontrouEu { todos = append(todos, b.id) }

	sort.Strings(todos)

	idxDono := -1
	for i, br := range todos {
		if br == donoOriginal {
			idxDono = i
			break
		}
	}

	if idxDono == -1 { return false }

	n := len(todos)
	for i := 1; i <= n; i++ {
		idx := (idxDono - i) % n
		if idx < 0 {
			idx += n
		}
		candidato := todos[idx]
		
		vivo := true
		if candidato != b.id {
			b.brokersMortosMu.RLock()
			vivo = !b.brokersMortos[candidato]
			b.brokersMortosMu.RUnlock()
		}

		if vivo {
			if candidato == b.id {
				b.logger.Printf("[FAILOVER ativado] Assumindo leitura do setor morto: %s", setorDaLeitura)
				return true
			}
			return false
		}
	}
	return false
}

func (b *Broker) processarLeitura(dados []byte) {
	var leitura models.LeituraSensor
	if err := json.Unmarshal(dados, &leitura); err != nil {
		return
	}

	b.logger.Printf("[UDP RECEBIDO] Leitura do Sensor %s, Setor %s, Criticidade %s, Vessel=%s", leitura.SensorID, leitura.SetorID, leitura.Criticidade.String(), leitura.VesselID)

	if !b.responsavelPorSetor(leitura.SetorID) {
		return
	}

	if leitura.Criticidade < models.CriticidadeAlta && leitura.Criticidade != models.CriticidadeBaixa {
		return
	}

	tempoLamport := b.tick()
	ocID := fmt.Sprintf("%s-%d", leitura.SensorID, leitura.Timestamp.UnixNano())
	
	oc := models.Ocorrencia{
		ID:           ocID,
		SetorOrigem:  leitura.SetorID,
		BrokerOrigem: b.id,
		Tipo:         leitura.Tipo,
		Descricao:    fmt.Sprintf("Sensor %s: %.2f %s", leitura.SensorID, leitura.Valor, leitura.Unidade),
		Criticidade:  leitura.Criticidade,
		Timestamp:    leitura.Timestamp,
		LamportTime:  tempoLamport,
		Posicao:      leitura.Posicao,
		VesselID:     leitura.VesselID,
	}

	if leitura.VesselID != "" {
		owner, registered := b.blockchain.GetVesselOwner(leitura.VesselID)
		if registered {
			oc.CompanyAddr = owner
			oc.Status = "AGUARDANDO_PAGAMENTO"
			
			// Determina posição do navio
			vesselPos := leitura.Posicao
			b.vesselsMu.RLock()
			if pos, exists := b.vesselPositions[leitura.VesselID]; exists {
				vesselPos = pos
			}
			b.vesselsMu.RUnlock()

			// Calcula cotação do drone mais próximo
			drone, found := b.droneMaisProximo(oc)
			if found {
				oc.CustoELIS = pricing.CalcularCusto(vesselPos, drone.Posicao)
			} else {
				oc.CustoELIS = 15.0 // fallback base cost
			}
			
			b.ocorrenciasMu.Lock()
			b.ocorrencias[oc.ID] = oc
			b.ocorrenciasMu.Unlock()

			b.logger.Printf("[OCORRÊNCIA NAVIO] %s requer escolta de %s. Custo calculado: %.2f ELIS. Aguardando pagamento...", oc.ID, owner, oc.CustoELIS)
			
			b.broadcastVizinhos(models.MensagemBroker{
				Tipo:        models.MsgRequisicaoDrone,
				BrokerID:    b.id,
				Ocorrencia:  &oc,
				Timestamp:   time.Now(),
				LamportTime: tempoLamport,
			})
			return
		}
	}

	// Ocorrência ambiental normal ou navio não registrado
	oc.Status = "ABERTA"
	b.ocorrenciasMu.Lock()
	b.ocorrencias[oc.ID] = oc
	b.ocorrenciasMu.Unlock()

	b.fila.Enfileirar(oc)
	b.logger.Printf("Ocorrência pública registrada: %s [%s] L=%d — %s", oc.ID, oc.Criticidade, tempoLamport, oc.Descricao)

	b.broadcastVizinhos(models.MensagemBroker{
		Tipo:        models.MsgRequisicaoDrone,
		BrokerID:    b.id,
		Ocorrencia:  &oc,
		Timestamp:   time.Now(),
		LamportTime: tempoLamport,
	})
}

// ── TCP — escuta Drones e Brokers ─────────────────────────────────────────────

func (b *Broker) escutarTCP() {
	ln, err := net.Listen("tcp", b.portaTCP)
	if err != nil {
		b.logger.Fatalf("TCP listen: %v", err)
	}
	defer ln.Close()
	b.logger.Printf("Escutando Drones/Brokers TCP em %s", b.portaTCP)

	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go b.identificarConexao(conn)
	}
}

func (b *Broker) identificarConexao(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	linha := scanner.Bytes()

	var md models.MensagemDrone
	if err := json.Unmarshal(linha, &md); err == nil && md.Tipo == models.DroneRegistro {
		b.logger.Printf("[DRONE MSG RECEBIDA] Registro do Drone %s na malha local", md.DroneID)
		b.registrarDroneLocal(md.DroneID, md.DroneInfo, conn)
		go b.loopLeituraDrone(md.DroneID, conn, scanner)
		return
	}

	var msg models.MensagemBroker
	if err := json.Unmarshal(linha, &msg); err == nil && (msg.Tipo == models.MsgRegistro || msg.Tipo == models.MsgDiscovery) {
		b.logger.Printf("[TCP RECEBIDO] Conexão inicial de %s: tipo=%s", msg.BrokerID, msg.Tipo)
		b.syncLamport(msg.LamportTime)
		b.registrarVizinho(msg.BrokerID, conn)
		b.syncMempoolComVizinho(msg.BrokerID, conn)
		b.enviarSincGlobal(conn)
		
		if strings.HasPrefix(msg.BrokerID, "MONITOR-") {
			b.peersConhecidosMu.Lock()
			listaPeers := make([]string, 0, len(b.peersConhecidos))
			for p := range b.peersConhecidos {
				listaPeers = append(listaPeers, p)
			}
			b.peersConhecidosMu.Unlock()

			resposta := models.MensagemBroker{
				Tipo:        models.MsgPeerList,
				BrokerID:    b.id,
				Peers:       listaPeers,
				Timestamp:   time.Now(),
				LamportTime: b.tick(),
			}
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			json.NewEncoder(conn).Encode(resposta)
		}
		
		b.processarMensagemBroker(msg, conn)
		go b.loopLeituraBroker(msg.BrokerID, conn, scanner)
		return
	}

	b.logger.Printf("Conexão desconhecida de %s — fechando", conn.RemoteAddr())
	conn.Close()
}

// ── Drones Locais ─────────────────────────────────────────────────────────────

func (b *Broker) registrarDroneLocal(droneID string, info *models.InfoDrone, conn net.Conn) {
	b.dronesLocaisMu.Lock()
	b.dronesLocais[droneID] = conn
	b.dronesLocaisMu.Unlock()

	if info != nil {
		info.BrokerID = b.id
		if info.Estado == models.DroneDisponivel {
			info.DisponiveisDesde = time.Now()
		}
		b.dronesMu.Lock()
		b.drones[droneID] = *info
		b.dronesMu.Unlock()

		b.logger.Printf("Drone %s registrado na malha local", droneID)
		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgSincDrone,
			BrokerID:    b.id,
			Drone:       info,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		})
	}
}

func (b *Broker) loopLeituraDrone(droneID string, conn net.Conn, scanner *bufio.Scanner) {
	defer func() {
		conn.Close()
		b.dronesLocaisMu.Lock()
		activeConn := b.dronesLocais[droneID]
		isActive := (activeConn == conn)
		if isActive {
			delete(b.dronesLocais, droneID)
		}
		b.dronesLocaisMu.Unlock()

		if !isActive {
			b.logger.Printf("Conexão antiga do Drone %s encerrada. Ignorando cleanup.", droneID)
			return
		}

		b.logger.Printf("Drone %s perdeu conexão (possível abate)", droneID)

		b.dronesMu.Lock()
		d, ok := b.drones[droneID]
		if ok {
			d.Estado = models.DroneAbatido
			d.UltimaVez = time.Now()
			b.drones[droneID] = d
		}
		b.dronesMu.Unlock()

		if ok {
			b.broadcastVizinhos(models.MensagemBroker{
				Tipo:        models.MsgDronePerdido,
				BrokerID:    b.id,
				DroneID:     droneID,
				Motivo:      "DESCONEXAO_TCP",
				Timestamp:   time.Now(),
				LamportTime: b.tick(),
			})
			b.tratarDronePerdido(d)
		}
	}()

	for scanner.Scan() {
		var msg models.MensagemDrone
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		b.logger.Printf("[DRONE MSG RECEBIDA] Drone %s envia tipo=%s, estado=%s", msg.DroneID, msg.Tipo, msg.NovoEstado)
		b.processarMensagemDrone(msg)
	}
}

func (b *Broker) processarMensagemDrone(msg models.MensagemDrone) {
	agora := time.Now()
	b.dronesMu.Lock()
	d, ok := b.drones[msg.DroneID]
	
	if ok && msg.Tipo == models.DroneKeepalive && msg.DroneInfo != nil {
		d.Posicao = msg.DroneInfo.Posicao
		d.UltimaVez = agora
		b.drones[msg.DroneID] = d
		
		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgSincDrone,
			BrokerID:    b.id,
			Drone:       &d,
			Timestamp:   agora,
			LamportTime: b.tick(),
		})
	}

	if ok && msg.Tipo == models.DroneEstado {
		d.Estado = msg.NovoEstado
		d.Posicao = msg.Posicao
		d.UltimaVez = agora
		if msg.NovoEstado == models.DroneDisponivel {
			d.OcorrenciaID = ""
			d.DisponiveisDesde = agora
		}
		if msg.NovoEstado == models.DroneDespachado || msg.NovoEstado == models.DroneEmMissao {
			d.DisponiveisDesde = time.Time{}
			if msg.OcorrenciaID != "" {
				d.OcorrenciaID = msg.OcorrenciaID
			}
		}
		b.drones[msg.DroneID] = d
		b.logger.Printf("Drone %s → %s", msg.DroneID, msg.NovoEstado)

		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgSincDrone,
			BrokerID:    b.id,
			Drone:       &d,
			Timestamp:   agora,
			LamportTime: b.tick(),
		})

		if msg.NovoEstado == models.DroneDisponivel && msg.OcorrenciaID != "" {
			b.broadcastVizinhos(models.MensagemBroker{
				Tipo:         models.MsgMissaoConcluida,
				BrokerID:     b.id,
				DroneID:      msg.DroneID,
				OcorrenciaID: msg.OcorrenciaID,
				Timestamp:    agora,
				LamportTime:  b.tick(),
			})
			
			// Registra log de conclusão de missão on-chain
			b.ocorrenciasMu.RLock()
			occ, exists := b.ocorrencias[msg.OcorrenciaID]
			b.ocorrenciasMu.RUnlock()
			if exists && occ.VesselID != "" {
				tx := models.Transaction{
					Type:         models.TxMissionLog,
					From:         wallet.GetAddress(blockchain.GetValidatorPubKey(b.id)),
					OccurrenceID: msg.OcorrenciaID,
					VesselID:     occ.VesselID,
					DroneID:      msg.DroneID,
					Payload:      fmt.Sprintf("Escort successfully completed at coordinates (%.1f, %.1f)", d.Posicao.X, d.Posicao.Y),
					Timestamp:    time.Now(),
				}
				tx.ID = blockchain.HashTx(tx)
				b.blockchain.AddTxToMempool(tx)
				
				b.broadcastVizinhos(models.MensagemBroker{
					Tipo:        models.MsgTxBroadcast,
					BrokerID:    b.id,
					Transaction: &tx,
					Timestamp:   time.Now(),
					LamportTime: b.tick(),
				})
				b.logger.Printf("[ON-CHAIN LOG] Tx de conclusão registrada para Ocorrência %s", msg.OcorrenciaID)
			}
		}

		if msg.NovoEstado == models.DroneAbatido {
			b.tratarDronePerdido(d)
		}
	}
	b.dronesMu.Unlock()
}

func (b *Broker) tratarDronePerdido(d models.InfoDrone) {
	if d.OcorrenciaID != "" {
		b.logger.Printf("CRÍTICO: Drone %s abatido/perdido em missão! Re-enfileirando %s", d.DroneID, d.OcorrenciaID)
		
		b.atendidosMu.Lock()
		b.atendidos[d.OcorrenciaID] = false
		b.atendidosMu.Unlock()

		b.ocorrenciasMu.RLock()
		oc, ok := b.ocorrencias[d.OcorrenciaID]
		b.ocorrenciasMu.RUnlock()

		if ok {
			b.fila.Enfileirar(oc)
		}
	}
}

// ── Despacho por proximidade ──────────────────────────────────────────────────

func (b *Broker) droneMaisProximo(oc models.Ocorrencia) (models.InfoDrone, bool) {
	b.dronesMu.RLock()
	defer b.dronesMu.RUnlock()

	var melhor models.InfoDrone
	encontrou := false
	var menorDist float64

	for _, d := range b.drones {
		if !d.Disponivel() {
			continue
		}
		dist := oc.Posicao.Distancia(d.Posicao)
		if !encontrou || dist < menorDist {
			melhor = d
			menorDist = dist
			encontrou = true
		}
	}
	return melhor, encontrou
}

func (b *Broker) marcarOcupado(droneID, ocorrenciaID string) {
	b.dronesMu.Lock()
	defer b.dronesMu.Unlock()
	if d, ok := b.drones[droneID]; ok {
		d.Estado = models.DroneDespachado
		d.OcorrenciaID = ocorrenciaID
		d.UltimaVez = time.Now()
		d.DisponiveisDesde = time.Time{}
		b.drones[droneID] = d
	}
}

// ── Loop de despacho (Exclusão Mútua Distribuída) ─────────────────────────────

func (b *Broker) loopDespachar() {
	ticker := time.NewTicker(despachoInterval)
	defer ticker.Stop()
	for range ticker.C {
		b.tentarDespachar()
	}
}

func (b *Broker) tentarDespachar() {
	oc, ok := b.fila.Peek()
	if !ok {
		return
	}

	b.atendidosMu.Lock()
	jaAtendida := b.atendidos[oc.ID]
	b.atendidosMu.Unlock()
	if jaAtendida {
		b.fila.Remover(oc.ID)
		return
	}
	
	b.dronesLocaisMu.RLock()
	var droneAlvo string
	var conn net.Conn
	for id, c := range b.dronesLocais {
		b.dronesMu.RLock()
		d, ok := b.drones[id]
		b.dronesMu.RUnlock()
		if ok && d.Disponivel() {
			droneAlvo = id
			conn = c
			break
		}
	}
	b.dronesLocaisMu.RUnlock()

	if droneAlvo != "" {
		b.fila.Desenfileirar()
		b.marcarOcupado(droneAlvo, oc.ID)

		// Coordenada do drone escoltando o navio fisicamente
		alvoCoordenada := oc.Posicao
		if oc.VesselID != "" {
			b.vesselsMu.RLock()
			if pos, exists := b.vesselPositions[oc.VesselID]; exists {
				alvoCoordenada = pos
			}
			b.vesselsMu.RUnlock()
		}

		cmd := models.ComandoDrone{
			Tipo:         models.CmdDespacharDrone,
			OcorrenciaID: oc.ID,
			SetorDestino: oc.SetorOrigem,
			PosicaoAlvo:  alvoCoordenada,
			Timestamp:    time.Now(),
		}

		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := json.NewEncoder(conn).Encode(cmd); err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				b.logger.Printf("Erro ao enviar comando para drone %s: %v. Fechando conexão.", droneAlvo, err)
			}
			conn.Close()

			b.atendidosMu.Lock()
			b.atendidos[oc.ID] = false
			b.atendidosMu.Unlock()
			b.fila.Enfileirar(oc)
		} else {
			b.logger.Printf("[DRONE COMANDO ENVIADO] Para drone %s, escoltando %s em (%.1f, %.1f)", droneAlvo, oc.VesselID, alvoCoordenada.X, alvoCoordenada.Y)

			b.atendidosMu.Lock()
			b.atendidos[oc.ID] = true
			b.atendidosMu.Unlock()

			b.dronesMu.RLock()
			dInfo := b.drones[droneAlvo]
			b.dronesMu.RUnlock()
			
			b.broadcastVizinhos(models.MensagemBroker{
				Tipo:         models.MsgDroneDespachado,
				BrokerID:     b.id,
				DroneID:      droneAlvo,
				OcorrenciaID: oc.ID,
				Drone:        &dInfo,
				Timestamp:    time.Now(),
				LamportTime:  b.tick(),
			})
		}
	}
}

// ── Ociosidade ────────────────────────────────────────────────────────────────

func (b *Broker) loopVerificarOciosidade() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		agora := time.Now()
		b.dronesMu.RLock()
		for _, d := range b.drones {
			if d.Estado != models.DroneDisponivel || d.DisponiveisDesde.IsZero() {
				continue
			}
			ocioso := agora.Sub(d.DisponiveisDesde)
			if ocioso > ociosidadeTimeout {
				b.logger.Printf("[OCIOSIDADE] Drone %s disponível há %s sem despacho",
					d.DroneID, ocioso.Round(time.Second))
			}
		}
		b.dronesMu.RUnlock()
	}
}

// ── Blockchain / Pagamentos / Consenso PoA ─────────────────────────────────────

func (b *Broker) pagarOcorrencia(occID string, from string, sig string) (string, error) {
	b.ocorrenciasMu.Lock()
	occ, exists := b.ocorrencias[occID]
	b.ocorrenciasMu.Unlock()
	if !exists {
		return "", fmt.Errorf("ocorrência não encontrada")
	}

	owner, ok := b.blockchain.GetVesselOwner(occ.VesselID)
	if !ok || owner != from {
		return "", fmt.Errorf("permissão negada: navio %s pertence a outra empresa", occ.VesselID)
	}

	pubKey, ok := b.blockchain.GetCompanyPubKey(from)
	if !ok {
		return "", fmt.Errorf("carteira da empresa não encontrada na blockchain")
	}

	if !wallet.Verify(pubKey, []byte(occID), sig) {
		return "", fmt.Errorf("assinatura digital de pagamento inválida")
	}

	brokerPubKey := blockchain.GetValidatorPubKey(b.id)
	brokerAddr := wallet.GetAddress(brokerPubKey)

	tx := models.Transaction{
		Type:         models.TxTransfer,
		From:         from,
		To:           brokerAddr,
		PublicKey:    pubKey,
		Amount:       occ.CustoELIS,
		OccurrenceID: occID,
		VesselID:     occ.VesselID,
		Payload:      fmt.Sprintf("Pagamento Escolta Ocorrencia %s", occID),
		Timestamp:    time.Now(),
		Signature:    sig,
	}
	tx.ID = blockchain.HashTx(tx)

	if err := b.blockchain.AddTxToMempool(tx); err != nil {
		return "", err
	}

	b.broadcastVizinhos(models.MensagemBroker{
		Tipo:        models.MsgTxBroadcast,
		BrokerID:    b.id,
		Transaction: &tx,
		Timestamp:   time.Now(),
		LamportTime: b.tick(),
	})

	b.logger.Printf("[MEMPOOL] Transação de Pagamento %s de %.2f ELIS para Ocorrência %s adicionada por %s", tx.ID[:8], tx.Amount, occID, from[:8])
	return tx.ID, nil
}

func (b *Broker) loopConsensus() {
	ticker := time.NewTicker(consensusInterval)
	defer ticker.Stop()
	for range ticker.C {
		blocks := b.blockchain.GetBlocks()
		mempool := b.blockchain.GetMempool()

		nextIndex := len(blocks)
		proposer := blockchain.GetProposer(nextIndex)

		if proposer == b.id && len(mempool) > 0 {
			txs := mempool
			if len(txs) > 5 {
				txs = txs[:5]
			}

			b.logger.Printf("[CONSENSO POA] Propondo Bloco %d como validador da vez. Transações: %d", nextIndex, len(txs))

			block := models.Block{
				Index:        nextIndex,
				Timestamp:    time.Now(),
				Transactions: txs,
				PrevHash:     blocks[nextIndex-1].Hash,
				Validator:    b.id,
			}
			block.Hash = blockchain.HashBlock(block)

			privKey := blockchain.GetValidatorPrivKey(b.id)
			err := blockchain.SignBlock(&block, privKey)
			if err != nil {
				continue
			}

			b.consensus.mu.Lock()
			b.consensus.proposals[block.Hash] = block
			myVote := fmt.Sprintf("%s:%s", b.id, block.Signature)
			b.consensus.votes[block.Hash] = []string{myVote}
			b.consensus.mu.Unlock()

			b.broadcastVizinhos(models.MensagemBroker{
				Tipo:      models.MsgBlockProposal,
				BrokerID:  b.id,
				Block:     &block,
				Timestamp: time.Now(),
			})
		}
	}
}

func (b *Broker) handleBlockProposal(block models.Block) {
	expectedProposer := blockchain.GetProposer(block.Index)
	if expectedProposer != block.Validator {
		return
	}

	pubKey := blockchain.GetValidatorPubKey(block.Validator)
	if !blockchain.VerifyBlockSignature(block, pubKey) {
		return
	}

	blocks := b.blockchain.GetBlocks()
	latestBlock := blocks[len(blocks)-1]
	if block.Index != latestBlock.Index+1 || block.PrevHash != latestBlock.Hash {
		return
	}

	b.consensus.mu.Lock()
	if b.consensus.votedBlocks[block.Hash] {
		b.consensus.mu.Unlock()
		return
	}
	b.consensus.votedBlocks[block.Hash] = true
	b.consensus.mu.Unlock()

	b.logger.Printf("[CONSENSO POA] Proposta de bloco %d do validador %s aprovada. Enviando voto...", block.Index, block.Validator)

	privKey := blockchain.GetValidatorPrivKey(b.id)
	voteBlock := block
	err := blockchain.SignBlock(&voteBlock, privKey)
	if err != nil {
		return
	}

	b.broadcastVizinhos(models.MensagemBroker{
		Tipo:      models.MsgBlockVote,
		BrokerID:  b.id,
		Block:     &block,
		Payload:   voteBlock.Signature,
		Timestamp: time.Now(),
	})
}

func (b *Broker) handleBlockVote(voterID string, block models.Block, sig string) {
	b.consensus.mu.Lock()
	defer b.consensus.mu.Unlock()

	if block.Validator != b.id {
		return
	}

	votes := b.consensus.votes[block.Hash]
	voteStr := fmt.Sprintf("%s:%s", voterID, sig)

	for _, v := range votes {
		if strings.HasPrefix(v, voterID+":") {
			return
		}
	}

	votes = append(votes, voteStr)
	b.consensus.votes[block.Hash] = votes

	b.logger.Printf("[CONSENSO POA] Voto recebido de %s para Bloco %d. Votos: %d/4", voterID, block.Index, len(votes))

	if len(votes) >= 3 {
		commitBlock := block
		commitBlock.Signature = strings.Join(votes, ";")
		commitBlock.Hash = blockchain.HashBlock(commitBlock)

		err := b.blockchain.AddBlock(commitBlock)
		if err != nil {
			return
		}
		b.blockchain.Save()

		b.logger.Printf("[CONSENSO SUCESSO] Bloco %d comitado! Propagando commits...", commitBlock.Index)

		b.processarTransacoesBloco(commitBlock)

		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:      models.MsgBlockCommit,
			BrokerID:  b.id,
			Block:     &commitBlock,
			Timestamp: time.Now(),
		})

		delete(b.consensus.votes, block.Hash)
		delete(b.consensus.proposals, block.Hash)
	}
}

func (b *Broker) handleBlockCommit(block models.Block) {
	if !blockchain.VerifyBlockVotes(block) {
		return
	}

	err := b.blockchain.AddBlock(block)
	if err != nil {
		return
	}
	b.blockchain.Save()

	b.logger.Printf("[CONSENSO COMMIT] Bloco %d adicionado via Commit de %s!", block.Index, block.Validator)
	b.processarTransacoesBloco(block)
}

func (b *Broker) processarTransacoesBloco(block models.Block) {
	for _, tx := range block.Transactions {
		if tx.Type == models.TxTransfer && tx.OccurrenceID != "" {
			b.ocorrenciasMu.Lock()
			occ, exists := b.ocorrencias[tx.OccurrenceID]
			if exists {
				occ.Status = "PAGO"
				b.ocorrencias[tx.OccurrenceID] = occ
				b.ocorrenciasMu.Unlock()

				b.atendidosMu.Lock()
				b.atendidos[tx.OccurrenceID] = false
				b.atendidosMu.Unlock()

				// Agora sim, enfileira para despacho físico imediato
				b.fila.Enfileirar(occ)
				b.logger.Printf("[BLOCKCHAIN SUCESSO] Ocorrência de navio %s paga on-chain! Enfileirada para despacho do drone.", tx.OccurrenceID)
			} else {
				b.ocorrenciasMu.Unlock()
			}
		}
	}
}

// ── Brokers vizinhos ──────────────────────────────────────────────────────────

func (b *Broker) conectarLider(addr string) {
	_, portaLocal, _ := net.SplitHostPort(b.portaTCP)
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = "localhost"
	}
	
	var addrsToTry []string
	addrsToTry = append(addrsToTry, addr)
	for port := 6003; port >= 6000; port-- {
		fallbackAddr := fmt.Sprintf("%s:%d", host, port)
		if fallbackAddr != b.portaTCP {
			addrsToTry = append(addrsToTry, fallbackAddr)
		}
	}

	backoff := 2 * time.Second
	for {
		connected := false
		for _, targetAddr := range addrsToTry {
			conn, err := net.DialTimeout("tcp", targetAddr, 2*time.Second)
			if err != nil {
				continue
			}

			b.logger.Printf("Conectado ao peer de descoberta %s! Solicitando Discovery...", targetAddr)
			backoff = 2 * time.Second

			reg := models.MensagemBroker{
				Tipo:        models.MsgDiscovery,
				BrokerID:    b.id,
				SetorID:     b.setorID,
				Motivo:      portaLocal,
				Timestamp:   time.Now(),
				LamportTime: b.tick(),
			}
			if err := json.NewEncoder(conn).Encode(reg); err != nil {
				conn.Close()
				continue
			}
			b.logger.Printf("[TCP ENVIADO] Para peer de descoberta %s, mensagem tipo=%s", targetAddr, reg.Tipo)

			b.registrarVizinho("LIDER", conn)
			scanner := bufio.NewScanner(conn)
			b.loopLeituraBroker("LIDER", conn, scanner)

			b.logger.Printf("Conexão com peer de descoberta %s perdida", targetAddr)
			connected = true
			break
		}

		if !connected {
			b.logger.Printf("Falha ao conectar a qualquer peer de descoberta. Tentando novamente em %s...", backoff)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
		} else {
			time.Sleep(2 * time.Second)
		}
	}
}

func (b *Broker) conectarVizinhos(enderecos string) {
	for _, addr := range splitCSV(enderecos) {
		go b.conectarVizinho(addr, false)
	}
}

func (b *Broker) conectarVizinho(addr string, isDiscovery bool) {
	backoff := 2 * time.Second
	for {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			b.logger.Printf("Falha ao conectar vizinho %s: %v — retry em %s", addr, err, backoff)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		reg := models.MensagemBroker{
			Tipo:        models.MsgRegistro,
			BrokerID:    b.id,
			SetorID:     b.setorID,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		}
		if err := json.NewEncoder(conn).Encode(reg); err != nil {
			conn.Close()
			continue
		}
		b.logger.Printf("[TCP ENVIADO] Para vizinho %s, mensagem tipo=%s", addr, reg.Tipo)

		b.logger.Printf("Conectado ao vizinho %s", addr)
		backoff = 2 * time.Second

		chaveTemp := addr
		b.vizinhosMu.Lock()
		b.vizinhos[chaveTemp] = conn
		b.vizinhosMu.Unlock()

		scanner := bufio.NewScanner(conn)
		idReal := chaveTemp
		for {
			conn.SetReadDeadline(time.Now().Add(heartbeatTimeout))
			if !scanner.Scan() {
				break
			}
			var msg models.MensagemBroker
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			if msg.Tipo != models.MsgHeartbeat {
				b.logger.Printf("[TCP RECEBIDO] De %s, mensagem tipo=%s", msg.BrokerID, msg.Tipo)
			}
			if msg.BrokerID != "" && msg.BrokerID != idReal {
				b.vizinhosMu.Lock()
				delete(b.vizinhos, idReal)
				b.vizinhos[msg.BrokerID] = conn
				b.vizinhosMu.Unlock()
				idReal = msg.BrokerID
				b.syncMempoolComVizinho(idReal, conn)
			}
			b.processarMensagemBroker(msg, conn)
		}

		b.vizinhosMu.Lock()
		if b.vizinhos[idReal] == conn {
			delete(b.vizinhos, idReal)
		}
		b.vizinhosMu.Unlock()
		conn.Close()
		
		b.logger.Printf("Vizinho %s desconectou — reconectando em %s", addr, backoff)
		time.Sleep(backoff)
		backoff = 2 * time.Second
	}
}

func (b *Broker) registrarVizinho(brokerID string, conn net.Conn) {
	b.vizinhosMu.Lock()
	b.vizinhos[brokerID] = conn
	b.vizinhosMu.Unlock()
	
	if eIDBrokerValido(brokerID) {
		b.heartbeatMu.Lock()
		b.ultimoHB[brokerID] = time.Now()
		b.heartbeatMu.Unlock()
	}
	
	b.logger.Printf("Broker vizinho registrado: %s (%s)", brokerID, conn.RemoteAddr())
}

func (b *Broker) syncMempoolComVizinho(brokerID string, conn net.Conn) {
	if !eIDBrokerValido(brokerID) {
		return
	}
	mempool := b.blockchain.GetMempool()
	if len(mempool) > 0 {
		b.logger.Printf("[SYNC MEMPOOL] Sincronizando %d transações na mempool com o vizinho %s", len(mempool), brokerID)
		go func() {
			for _, tx := range mempool {
				msg := models.MensagemBroker{
					Tipo:        models.MsgTxBroadcast,
					BrokerID:    b.id,
					Transaction: &tx,
					Timestamp:   time.Now(),
					LamportTime: b.tick(),
				}
				conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
				_ = json.NewEncoder(conn).Encode(msg)
			}
		}()
	}
}

func (b *Broker) obterPortaLocal() string {
	_, port, err := net.SplitHostPort(b.portaTCP)
	if err != nil {
		return strings.TrimPrefix(b.portaTCP, ":")
	}
	return port
}

func (b *Broker) eMeuProprioEndereco(peer string) bool {
	_, peerPort, err := net.SplitHostPort(peer)
	if err != nil {
		return false
	}
	return peerPort == b.obterPortaLocal()
}

func (b *Broker) deveIniciarConexao(peer string) bool {
	_, peerPort, err := net.SplitHostPort(peer)
	if err != nil {
		return false
	}
	return b.obterPortaLocal() < peerPort
}

func eIDBrokerValido(id string) bool {
	if len(id) < 2 || id[0] != 'B' {
		return false
	}
	for i := 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func (b *Broker) loopLeituraBroker(brokerID string, conn net.Conn, scanner *bufio.Scanner) {
	idReal := brokerID
	defer func() {
		conn.Close()
		b.vizinhosMu.Lock()
		if b.vizinhos[idReal] == conn {
			delete(b.vizinhos, idReal)
			b.logger.Printf("Broker vizinho desconectado: %s", idReal)
		} else {
			b.logger.Printf("Conexão antiga do Broker vizinho %s encerrada. Ignorando cleanup.", idReal)
		}
		b.vizinhosMu.Unlock()
	}()
	for {
		if !strings.HasPrefix(idReal, "MONITOR-") {
			conn.SetReadDeadline(time.Now().Add(heartbeatTimeout))
		}
		if !scanner.Scan() {
			break
		}
		var msg models.MensagemBroker
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Tipo != models.MsgHeartbeat {
			b.logger.Printf("[TCP RECEBIDO] De %s, mensagem tipo=%s", msg.BrokerID, msg.Tipo)
		}
		if msg.BrokerID != "" && msg.BrokerID != idReal {
			b.vizinhosMu.Lock()
			delete(b.vizinhos, idReal)
			b.vizinhos[msg.BrokerID] = conn
			b.vizinhosMu.Unlock()
			idReal = msg.BrokerID
		}
		b.processarMensagemBroker(msg, conn)
	}
}

func (b *Broker) processarMensagemBroker(msg models.MensagemBroker, conn net.Conn) {
	b.syncLamport(msg.LamportTime)
	b.enviarParaMonitores(msg)

	if eIDBrokerValido(msg.BrokerID) {
		b.heartbeatMu.Lock()
		b.ultimoHB[msg.BrokerID] = time.Now()
		b.heartbeatMu.Unlock()
	}

	if msg.SetorID != "" {
		b.setoresConhecidosMu.Lock()
		b.setoresConhecidos[msg.BrokerID] = msg.SetorID
		b.setoresConhecidosMu.Unlock()
	}

	b.brokersMortosMu.Lock()
	if b.brokersMortos[msg.BrokerID] {
		setorRecuperado := msg.SetorID
		if setorRecuperado == "" {
			b.setoresConhecidosMu.RLock()
			setorRecuperado = b.setoresConhecidos[msg.BrokerID]
			b.setoresConhecidosMu.RUnlock()
		}
		b.logger.Printf("Broker %s voltou à vida! Retornando o controle do setor %s para ele.", msg.BrokerID, setorRecuperado)
		b.brokersMortos[msg.BrokerID] = false
		b.broadcastVizinhos(models.MensagemBroker{
			Tipo:        models.MsgFailoverRecuperado,
			BrokerID:    msg.BrokerID,
			SetorID:     setorRecuperado,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		})
	}
	b.brokersMortosMu.Unlock()

	switch msg.Tipo {
	case models.MsgEleicao:
		b.logger.Printf("[ELEIÇÃO] Recebida MsgEleicao de %s", msg.BrokerID)
		meuNum := obterNumID(b.id)
		remotoNum := obterNumID(msg.BrokerID)
		if meuNum > remotoNum {
			okMsg := models.MensagemBroker{
				Tipo:        models.MsgEleicaoOk,
				BrokerID:    b.id,
				Timestamp:   time.Now(),
				LamportTime: b.tick(),
			}
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			json.NewEncoder(conn).Encode(okMsg)

			go b.iniciarEleicao()
		}

	case models.MsgEleicaoOk:
		b.logger.Printf("[ELEIÇÃO] Recebido OK de eleição do broker %s", msg.BrokerID)
		b.eleicaoMu.Lock()
		b.okRecebido = true
		b.eleicaoMu.Unlock()

	case models.MsgCoordenador:
		b.logger.Printf("[ELEIÇÃO] Novo coordenador anunciado: %s", msg.BrokerID)
		b.liderIDMu.Lock()
		b.liderID = msg.BrokerID
		b.statusEleicao = "ESTAVEL"
		b.liderIDMu.Unlock()

	case models.MsgHeartbeat:
		// heartbeat já tratado no cabeçalho
	case models.MsgDiscovery:
		portaRemota := msg.Motivo
		host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		peerAddr := host + ":" + portaRemota
		
		b.peersConhecidosMu.Lock()
		b.peersConhecidos[peerAddr] = true
		listaPeers := make([]string, 0, len(b.peersConhecidos))
		for p := range b.peersConhecidos {
			if p != peerAddr {
				listaPeers = append(listaPeers, p)
			}
		}
		b.peersConhecidosMu.Unlock()

		b.logger.Printf("Líder registrou novo peer %s (Broker %s). Enviando lista atualizada.", peerAddr, msg.BrokerID)
		
		resposta := models.MensagemBroker{
			Tipo:        models.MsgPeerList,
			BrokerID:    b.id,
			Peers:       listaPeers,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		}
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := json.NewEncoder(conn).Encode(resposta); err != nil {
			conn.Close()
		}

		broadcast := models.MensagemBroker{
			Tipo:        models.MsgPeerList,
			BrokerID:    b.id,
			Peers:       []string{peerAddr},
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		}
		b.broadcastVizinhos(broadcast)
		
	case models.MsgPeerList:
		for _, peer := range msg.Peers {
			b.peersConhecidosMu.Lock()
			jaConhece := b.peersConhecidos[peer]
			if !jaConhece {
				b.peersConhecidos[peer] = true
			}
			b.peersConhecidosMu.Unlock()
			
			if !jaConhece {
				if b.eMeuProprioEndereco(peer) {
					continue
				}
				if b.deveIniciarConexao(peer) {
					b.logger.Printf("Descoberto novo peer via Líder: %s. Conectando...", peer)
					go b.conectarVizinho(peer, true)
				}
			}
		}
	case models.MsgSincDrone:
		if msg.Drone == nil { return }
		b.dronesMu.Lock()
		existing, ok := b.drones[msg.Drone.DroneID]
		if !ok || msg.Drone.UltimaVez.After(existing.UltimaVez) {
			if ok && msg.Drone.Estado == models.DroneDisponivel && existing.DisponiveisDesde.IsZero() {
				msg.Drone.DisponiveisDesde = time.Now()
			}
			b.drones[msg.Drone.DroneID] = *msg.Drone
		}
		b.dronesMu.Unlock()
	case models.MsgMissaoConcluida:
		b.logger.Printf("Missão concluída: drone %s liberou ocorrência %s", msg.DroneID, msg.OcorrenciaID)
		b.ocorrenciasMu.Lock()
		delete(b.ocorrencias, msg.OcorrenciaID)
		b.ocorrenciasMu.Unlock()
	case models.MsgRequisicaoDrone:
		if msg.Ocorrencia == nil || msg.Ocorrencia.Criticidade == models.CriticidadeNula { return }
		oc := *msg.Ocorrencia
		b.ocorrenciasMu.Lock()
		b.ocorrencias[oc.ID] = oc
		b.ocorrenciasMu.Unlock()

		b.atendidosMu.Lock()
		jaAtendida := b.atendidos[oc.ID]
		b.atendidosMu.Unlock()
		
		if !jaAtendida && oc.VesselID == "" {
			b.fila.Enfileirar(oc)
		}
	case models.MsgDroneDespachado:
		b.atendidosMu.Lock()
		b.atendidos[msg.OcorrenciaID] = true
		b.atendidosMu.Unlock()
		b.fila.Remover(msg.OcorrenciaID)
		b.logger.Printf("Ocorrência %s atendida por %s (drone %s)", msg.OcorrenciaID, msg.BrokerID, msg.DroneID)
		if msg.Drone != nil {
			b.dronesMu.Lock()
			b.drones[msg.DroneID] = *msg.Drone
			b.dronesMu.Unlock()
		}
	case models.MsgDronePerdido:
		b.logger.Printf("Drone %s perdido: %s (broker %s)", msg.DroneID, msg.Motivo, msg.BrokerID)
		b.dronesMu.Lock()
		if d, ok := b.drones[msg.DroneID]; ok {
			d.Estado = models.DroneAbatido
			d.UltimaVez = time.Now()
			b.drones[msg.DroneID] = d
			b.tratarDronePerdido(d)
		}
		b.dronesMu.Unlock()

	// Blockchain e Consensus handlers
	case models.MsgTxBroadcast:
		if msg.Transaction != nil {
			b.blockchain.AddTxToMempool(*msg.Transaction)
		}
	case models.MsgBlockProposal:
		if msg.Block != nil {
			b.handleBlockProposal(*msg.Block)
		}
	case models.MsgBlockVote:
		if msg.Block != nil {
			b.handleBlockVote(msg.BrokerID, *msg.Block, msg.Payload)
		}
	case models.MsgBlockCommit:
		if msg.Block != nil {
			b.handleBlockCommit(*msg.Block)
		}
	case models.MsgChainSync:
		b.logger.Printf("[SYNC] Solicitado chain sync de %s. Enviando nossos %d blocos...", msg.BrokerID, len(b.blockchain.GetBlocks()))
		resposta := models.MensagemBroker{
			Tipo:        models.MsgChainResponse,
			BrokerID:    b.id,
			ChainBlocks: b.blockchain.GetBlocks(),
			Timestamp:   time.Now(),
		}
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		json.NewEncoder(conn).Encode(resposta)

	case models.MsgChainResponse:
		if len(msg.ChainBlocks) > len(b.blockchain.GetBlocks()) {
			b.logger.Printf("[SYNC] Adotando blockchain com %d blocos do broker %s (atual local: %d)", len(msg.ChainBlocks), msg.BrokerID, len(b.blockchain.GetBlocks()))
			
			// Validação rápida do Genesis block hash
			if msg.ChainBlocks[0].Hash == b.blockchain.GetBlocks()[0].Hash {
				b.blockchain.ReplaceChain(msg.ChainBlocks)
				b.blockchain.Save()
				b.logger.Printf("[SYNC SUCESSO] Blockchain sincronizada e persistida!")
			}
		}

	case models.MsgVesselRegistered:
		// Sincroniza posição do navio entre brokers
		var x, y float64
		_, err := fmt.Sscanf(msg.Payload, "%f,%f", &x, &y)
		if err == nil {
			b.vesselsMu.Lock()
			b.vesselPositions[msg.VesselID] = models.Coordenada{X: x, Y: y}
			b.vesselLastSeen[msg.VesselID] = time.Now()
			b.vesselsMu.Unlock()
		}
	}
}

// ── Sincronização global de drones ────────────────────────────────────────────

func (b *Broker) enviarSincGlobal(conn net.Conn) {
	b.dronesMu.RLock()
	drones := make([]models.InfoDrone, 0, len(b.drones))
	for _, d := range b.drones {
		drones = append(drones, d)
	}
	b.dronesMu.RUnlock()

	for i := range drones {
		d := drones[i]
		msg := models.MensagemBroker{
			Tipo:        models.MsgSincDrone,
			BrokerID:    b.id,
			Drone:       &d,
			Timestamp:   time.Now(),
			LamportTime: b.tick(),
		}
		conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := json.NewEncoder(conn).Encode(msg); err != nil {
			conn.Close()
			return
		}
	}
}

// ── Heartbeat e detecção de falhas ────────────────────────────────────────────

func (b *Broker) loopHeartbeat() {
	ticker := time.NewTicker(heartbeatInterval / 2)
	defer ticker.Stop()
	for range ticker.C {
		b.liderIDMu.RLock()
		lID := b.liderID
		sEl := b.statusEleicao
		b.liderIDMu.RUnlock()

		hb := models.MensagemBroker{
			Tipo:          models.MsgHeartbeat,
			BrokerID:      b.id,
			SetorID:       b.setorID,
			LiderID:       lID,
			StatusEleicao: sEl,
			Timestamp:     time.Now(),
			LamportTime:   b.tick(),
		}
		b.broadcastVizinhos(hb)
	}
}

func (b *Broker) loopDetectarFalhas() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for range ticker.C {
		agora := time.Now()
		b.heartbeatMu.Lock()
		for id, ultimo := range b.ultimoHB {
			if id == "LIDER" {
				continue
			}
			if agora.Sub(ultimo) > heartbeatTimeout {
				b.brokersMortosMu.Lock()
				if !b.brokersMortos[id] {
					b.logger.Printf("Broker %s presumido morto (sem HB há %s). Ativando rotinas de Failover!", id, agora.Sub(ultimo).Round(time.Second))
					b.brokersMortos[id] = true
					go b.verificarEAtivarFailover(id)

					b.liderIDMu.RLock()
					lCur := b.liderID
					b.liderIDMu.RUnlock()
					if id == lCur {
						b.logger.Printf("[ELEIÇÃO] Líder %s caiu. Iniciando processo de eleição!", id)
						go b.iniciarEleicao()
					}
				}
				b.brokersMortosMu.Unlock()
			}
		}
		b.heartbeatMu.Unlock()
	}
}

func (b *Broker) verificarEAtivarFailover(deadBrokerID string) {
	b.setoresConhecidosMu.RLock()
	setorMorto, ok := b.setoresConhecidos[deadBrokerID]
	b.setoresConhecidosMu.RUnlock()
	if !ok {
		return
	}

	b.setoresConhecidosMu.RLock()
	var todos []string
	for brokerID := range b.setoresConhecidos {
		todos = append(todos, brokerID)
	}
	b.setoresConhecidosMu.RUnlock()

	encontrouEu := false
	for _, br := range todos {
		if br == b.id {
			encontrouEu = true
			break
		}
	}
	if !encontrouEu {
		todos = append(todos, b.id)
	}

	sort.Strings(todos)

	idxDono := -1
	for i, br := range todos {
		if br == deadBrokerID {
			idxDono = i
			break
		}
	}
	if idxDono == -1 {
		return
	}

	n := len(todos)
	for i := 1; i <= n; i++ {
		idx := (idxDono - i) % n
		if idx < 0 {
			idx += n
		}
		candidato := todos[idx]

		vivo := true
		if candidato != b.id {
			b.brokersMortosMu.RLock()
			vivo = !b.brokersMortos[candidato]
			b.brokersMortosMu.RUnlock()
		}

		if vivo {
			if candidato == b.id {
				b.logger.Printf("[FAILOVER ATIVADO] Eu (%s) assumo o setor '%s' do broker morto '%s'!", b.id, setorMorto, deadBrokerID)
				b.broadcastVizinhos(models.MensagemBroker{
					Tipo:        models.MsgFailover,
					BrokerID:    b.id,
					SetorID:     setorMorto,
					Motivo:      deadBrokerID,
					Timestamp:   time.Now(),
					LamportTime: b.tick(),
				})
			}
			break
		}
	}
}

func obterNumID(id string) int {
	var n int
	_, err := fmt.Sscanf(id, "B%d", &n)
	if err != nil {
		return 0
	}
	return n
}

func (b *Broker) iniciarEleicao() {
	b.eleicaoMu.Lock()
	if b.statusEleicao == "EM_ELEICAO" {
		b.eleicaoMu.Unlock()
		return
	}
	b.statusEleicao = "EM_ELEICAO"
	b.okRecebido = false
	b.eleicaoMu.Unlock()

	b.logger.Printf("[ELEIÇÃO] Iniciando processo de eleição. Meu ID: %s", b.id)

	b.broadcastVizinhos(models.MensagemBroker{
		Tipo:          models.MsgEleicao,
		BrokerID:      b.id,
		StatusEleicao: "EM_ELEICAO",
		LiderID:       "SELECIONANDO...",
		Timestamp:     time.Now(),
		LamportTime:   b.tick(),
	})

	meuNum := obterNumID(b.id)
	enviouParaMaior := false

	b.vizinhosMu.RLock()
	for id, conn := range b.vizinhos {
		if !eIDBrokerValido(id) {
			continue
		}
		numVizinho := obterNumID(id)
		if numVizinho > meuNum {
			enviouParaMaior = true
			msg := models.MensagemBroker{
				Tipo:        models.MsgEleicao,
				BrokerID:    b.id,
				Timestamp:   time.Now(),
				LamportTime: b.tick(),
			}
			go func(c net.Conn) {
				c.SetWriteDeadline(time.Now().Add(2 * time.Second))
				json.NewEncoder(c).Encode(msg)
			}(conn)
		}
	}
	b.vizinhosMu.RUnlock()

	if !enviouParaMaior {
		b.logger.Printf("[ELEIÇÃO] Sou o broker de maior ID ativo (%s). Me declarando líder!", b.id)
		b.declararLider()
		return
	}

	go func() {
		time.Sleep(2 * time.Second)
		b.eleicaoMu.Lock()
		defer b.eleicaoMu.Unlock()

		if b.statusEleicao == "EM_ELEICAO" && !b.okRecebido {
			b.logger.Printf("[ELEIÇÃO] Timeout aguardando OK dos brokers maiores. Me declarando líder!")
			b.declararLider()
		} else if b.statusEleicao == "EM_ELEICAO" && b.okRecebido {
			b.logger.Printf("[ELEIÇÃO] OK recebido de broker maior. Aguardando MsgCoordenador...")
			go func() {
				time.Sleep(4 * time.Second)
				b.eleicaoMu.Lock()
				defer b.eleicaoMu.Unlock()
				if b.statusEleicao == "EM_ELEICAO" {
					b.logger.Printf("[ELEIÇÃO] Novo coordenador não se anunciou. Reiniciando eleição...")
					b.statusEleicao = "ESTAVEL"
					go b.iniciarEleicao()
				}
			}()
		}
	}()
}

func (b *Broker) declararLider() {
	b.liderIDMu.Lock()
	b.liderID = b.id
	b.statusEleicao = "ESTAVEL"
	b.liderIDMu.Unlock()

	b.logger.Printf("[ELEIÇÃO] Eleição concluída! Eu (%s) sou o novo líder de descoberta.", b.id)

	msg := models.MensagemBroker{
		Tipo:          models.MsgCoordenador,
		BrokerID:      b.id,
		LiderID:       b.id,
		StatusEleicao: "ESTAVEL",
		Timestamp:     time.Now(),
		LamportTime:   b.tick(),
	}
	b.broadcastVizinhos(msg)
}

func (b *Broker) loopEnvelhecerFila() {
	ticker := time.NewTicker(envelhecerInterval)
	defer ticker.Stop()
	for range ticker.C {
		b.fila.Envelhecer()
	}
}

// ── Broadcast e utilitários ───────────────────────────────────────────────────

func (b *Broker) broadcastVizinhos(msg models.MensagemBroker) {
	b.vizinhosMu.RLock()
	defer b.vizinhosMu.RUnlock()
	for id, conn := range b.vizinhos {
		_ = id
		conn := conn
		go func() {
			conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := json.NewEncoder(conn).Encode(msg); err != nil {
				conn.Close()
			}
		}()
	}
}

func (b *Broker) enviarParaMonitores(msg models.MensagemBroker) {
	b.vizinhosMu.RLock()
	defer b.vizinhosMu.RUnlock()
	for id, conn := range b.vizinhos {
		if strings.HasPrefix(id, "MONITOR-") {
			conn := conn
			go func() {
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if err := json.NewEncoder(conn).Encode(msg); err != nil {
					conn.Close()
				}
			}()
		}
	}
}

func splitCSV(s string) []string {
	var res []string
	cur := ""
	for _, c := range s {
		if c == ',' {
			if cur != "" {
				res = append(res, cur)
				cur = ""
			}
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		res = append(res, cur)
	}
	return res
}

func (b *Broker) loopReposicaoPeriodica() {
	if b.id != "B1" {
		return
	}

	// We can check an environment variable or flag to enable/disable it
	// Let's check environment variable "ENABLE_TOKEN_REPLENISHMENT"
	// Default to false if not set, or if it is "false"
	
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		enabled := os.Getenv("ENABLE_TOKEN_REPLENISHMENT") == "true"
		if !enabled {
			continue
		}

		b.logger.Printf("[REPOSIÇÃO] Executando reposição periódica de tokens para empresas registradas...")
		compAddrs := b.blockchain.GetCompanyAddresses()
		for _, addr := range compAddrs {
			// Mint 500 ELIS
			tx := models.Transaction{
				Type:      models.TxMint,
				To:        addr,
				Amount:    500.0,
				Payload:   "Reabastecimento Periódico de Tokens",
				Timestamp: time.Now(),
			}
			tx.ID = blockchain.HashTx(tx)
			
			if err := b.blockchain.AddTxToMempool(tx); err != nil {
				b.logger.Printf("[REPOSIÇÃO] Erro ao adicionar TxMint de reposição: %v", err)
				continue
			}

			// Broadcast
			b.broadcastVizinhos(models.MensagemBroker{
				Tipo:        models.MsgTxBroadcast,
				BrokerID:    b.id,
				Transaction: &tx,
				Timestamp:   time.Now(),
				LamportTime: b.tick(),
			})
			b.logger.Printf("[REPOSIÇÃO] TxMint de 500.00 ELIS enviada para mempool para empresa %s", addr[:8])
		}
	}
}


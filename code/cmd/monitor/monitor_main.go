/*
Este arquivo implementa o Monitor Central do HormuzNet.
Ele serve como a central de controle e consolidação de visualização tática do Estreito.

Responsabilidades principais:
  - Conectar-se ao Broker Líder via TCP e descobrir automaticamente os demais
    Brokers da malha através do protocolo MsgPeerList (auto-discovery)
  - Coletar e de-duplicar eventos de todos os Brokers (status de Drones,
    ocorrências, Failovers e missões concluídas)
  - Detectar Brokers inativos por timeout de heartbeat
  - Expor um servidor HTTP com WebSocket RFC 6455 (implementado do zero, sem libs
    externas) na porta 8085 para atualizar o dashboard em tempo real
  - Servir o dashboard HTML/CSS/JS embutido com atualização automática a cada 1s
*/
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"HormuzNet/internal/blockchain"
	"HormuzNet/internal/models"
	"HormuzNet/internal/wallet"
)

// ── WebSocket RFC 6455 ────────────────────────────────────────────────────────

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func wsAccept(k string) string {
	h := sha1.New()
	h.Write([]byte(k + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func wsUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("hijack indisponível")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, nil, err
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + wsAccept(key) + "\r\n\r\n"
	rw.WriteString(resp)
	rw.Flush()
	return conn, rw, nil
}

func wsFrame(payload []byte) []byte {
	l := len(payload)
	var h []byte
	h = append(h, 0x81)
	switch {
	case l <= 125:
		h = append(h, byte(l))
	case l <= 65535:
		h = append(h, 126, byte(l>>8), byte(l))
	default:
		h = append(h, 127, 0, 0, 0, 0, byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
	}
	return append(h, payload...)
}

func wsLer(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	masked := hdr[1]&0x80 != 0
	plen := int(hdr[1] & 0x7F)
	switch plen {
	case 126:
		ext := make([]byte, 2)
		io.ReadFull(r, ext)
		plen = int(ext[0])<<8 | int(ext[1])
	case 127:
		ext := make([]byte, 8)
		io.ReadFull(r, ext)
		plen = int(ext[4])<<24 | int(ext[5])<<16 | int(ext[6])<<8 | int(ext[7])
	}
	var mk [4]byte
	if masked {
		io.ReadFull(r, mk[:])
	}
	payload := make([]byte, plen)
	io.ReadFull(r, payload)
	if masked {
		for i := range payload {
			payload[i] ^= mk[i%4]
		}
	}
	return payload, nil
}

// ── Hub de clientes WebSocket ─────────────────────────────────────────────────

type wsClient struct {
	conn net.Conn
	mu   sync.Mutex
}

type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
}

var hub = &Hub{clients: make(map[*wsClient]struct{})}

func (h *Hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *Hub) remove(c *wsClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.conn.Close()
}

func (h *Hub) broadcast(data []byte) {
	frame := wsFrame(data)
	h.mu.RLock()
	for c := range h.clients {
		c := c
		go func() {
			c.mu.Lock()
			c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			c.conn.Write(frame)
			c.mu.Unlock()
		}()
	}
	h.mu.RUnlock()
}

// ── Estado global do monitor ──────────────────────────────────────────────────

type BrokerStatus struct {
	ID       string    `json:"id"`
	Addr     string    `json:"addr"`
	Vivo     bool      `json:"vivo"`
	UltimoHB time.Time `json:"ultimo_hb"`
}

type EventoLog struct {
	Timestamp time.Time `json:"timestamp"`
	Tipo      string    `json:"tipo"`
	Mensagem  string    `json:"mensagem"`
	Nivel     string    `json:"nivel"` // info | warn | danger
}

type OcorrenciaDetalhada struct {
	ID          string    `json:"id"`
	Tipo        string    `json:"tipo"`
	Criticidade string    `json:"criticidade"`
	Status      string    `json:"status"`
	Timestamp   time.Time `json:"timestamp"`
	LamportTime int       `json:"lamport_time"`
}

type EstadoGlobal struct {
	Drones        map[string]models.InfoDrone       `json:"drones"`
	Brokers       []BrokerStatus                    `json:"brokers"`
	Eventos       []EventoLog                       `json:"eventos"`
	Ocorrencias   map[string]OcorrenciaDetalhada    `json:"ocorrencias"`
	Failovers     map[string]string                 `json:"failovers"`
	LiderAtual    string                            `json:"lider_atual"`
	StatusEleicao string                            `json:"status_eleicao"`
}

var (
	estadoMu      sync.RWMutex
	drones        = make(map[string]models.InfoDrone)
	brokers       = make(map[string]*BrokerStatus)
	eventos       []EventoLog
	ocorrencias   = make(map[string]OcorrenciaDetalhada)
	failovers     = make(map[string]string)
	liderAtual    = "B9" // B9 é o líder padrão
	statusEleicao = "ESTAVEL"
	liderMu       sync.RWMutex
)

func obterBrokerID(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch port {
	case "6000":
		return "B1"
	case "6001":
		return "B2"
	case "6002":
		return "B3"
	case "6003":
		return "B4"
	case "6004":
		return "B5"
	case "6005":
		return "B6"
	case "6006":
		return "B7"
	case "6007":
		return "B8"
	case "6008":
		return "B9"
	default:
		return "B_" + port
	}
}

func obterSetorPorBrokerID(brokerID string) string {
	switch brokerID {
	case "B1":
		return "Setor_Noroeste"
	case "B2":
		return "Setor_Nordeste"
	case "B3":
		return "Setor_Sudoeste"
	case "B4":
		return "Setor_Sudeste"
	default:
		return ""
	}
}

func addEvento(tipo, msg, nivel string) {
	estadoMu.Lock()
	eventos = append(eventos, EventoLog{
		Timestamp: time.Now(),
		Tipo:      tipo,
		Mensagem:  msg,
		Nivel:     nivel,
	})
	if len(eventos) > 100 {
		eventos = eventos[len(eventos)-100:]
	}
	estadoMu.Unlock()
}

func snapshot() []byte {
	estadoMu.RLock()
	blist := make([]BrokerStatus, 0, len(brokers))
	for _, b := range brokers {
		blist = append(blist, *b)
	}
	ev := make([]EventoLog, len(eventos))
	copy(ev, eventos)
	d := make(map[string]models.InfoDrone, len(drones))
	for k, v := range drones {
		d[k] = v
	}
	o := make(map[string]OcorrenciaDetalhada, len(ocorrencias))
	for k, v := range ocorrencias {
		o[k] = v
	}
	fo := make(map[string]string, len(failovers))
	for k, v := range failovers {
		fo[k] = v
	}
	estadoMu.RUnlock()

	liderMu.RLock()
	lCur := liderAtual
	sEl := statusEleicao
	liderMu.RUnlock()

	estado := EstadoGlobal{
		Drones:        d,
		Brokers:       blist,
		Eventos:       ev,
		Ocorrencias:   o,
		Failovers:     fo,
		LiderAtual:    lCur,
		StatusEleicao: sEl,
	}
	data, _ := json.Marshal(estado)
	return data
}

// ── Conexão com broker como observer ─────────────────────────────────────────

func conectarBroker(addr string) {
	backoff := 2 * time.Second
	for {
		conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			estadoMu.Lock()
			if b, ok := brokers[addr]; ok {
				b.Vivo = false
			}
			estadoMu.Unlock()
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}

		backoff = 2 * time.Second
		log.Printf("[MONITOR] Conectado ao broker %s", addr)

		// Registra como peer broker (ID especial MONITOR-...)
		reg := models.MensagemBroker{
			Tipo:      models.MsgRegistro,
			BrokerID:  "MONITOR-" + addr,
			Timestamp: time.Now(),
		}
		json.NewEncoder(conn).Encode(reg)

		estadoMu.Lock()
		if b, ok := brokers[addr]; !ok {
			brokers[addr] = &BrokerStatus{ID: obterBrokerID(addr), Addr: addr, Vivo: true, UltimoHB: time.Now()}
		} else {
			if b.ID == "" {
				b.ID = obterBrokerID(addr)
			}
			b.Vivo = true
			b.UltimoHB = time.Now()
		}
		estadoMu.Unlock()

		addEvento("CONEXAO", fmt.Sprintf("Conectado ao broker %s", addr), "info")

		scanner := bufio.NewScanner(conn)
		for {
			conn.SetReadDeadline(time.Now().Add(15 * time.Second))
			if !scanner.Scan() {
				break
			}
			var msg models.MensagemBroker
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			processarMensagem(msg, addr)
		}

		conn.Close()
		estadoMu.Lock()
		if b, ok := brokers[addr]; ok {
			b.Vivo = false
		}
		estadoMu.Unlock()
		addEvento("FALHA", fmt.Sprintf("Broker %s desconectou", addr), "danger")
		log.Printf("[MONITOR] Broker %s desconectou — reconectando em %s", addr, backoff)
		time.Sleep(backoff)
		backoff *= 2
	}
}

func processarMensagem(msg models.MensagemBroker, addr string) {
	estadoMu.Lock()
	if b, ok := brokers[addr]; ok {
		b.UltimoHB = time.Now()
	}

	// Registra/atualiza o broker que originou a mensagem
	if msg.BrokerID != "" && !strings.HasPrefix(msg.BrokerID, "MONITOR-") {
		bID := msg.BrokerID
		var bStatus *BrokerStatus
		for _, b := range brokers {
			if b.ID == bID || b.Addr == bID {
				bStatus = b
				break
			}
		}
		if bStatus != nil {
			bStatus.Vivo = true
			bStatus.UltimoHB = time.Now()

			// Se o broker está vivo, garante que o failover do seu setor original seja removido
			setor := obterSetorPorBrokerID(bID)
			if setor != "" {
				if _, isFailoverActive := failovers[setor]; isFailoverActive {
					delete(failovers, setor)
					eventos = append(eventos, EventoLog{
						Timestamp: time.Now(),
						Tipo:      "RECUPERACAO",
						Mensagem:  fmt.Sprintf("Broker %s voltou e recuperou o setor %s", bID, setor),
						Nivel:     "info",
					})
					if len(eventos) > 100 {
						eventos = eventos[len(eventos)-100:]
					}
				}
			}
		}
	}
	estadoMu.Unlock()

	switch msg.Tipo {
	case models.MsgHeartbeat:
		if msg.LiderID != "" {
			liderMu.Lock()
			liderAtual = msg.LiderID
			statusEleicao = msg.StatusEleicao
			liderMu.Unlock()
		}

	case models.MsgEleicao:
		addEvento("ELEICAO", fmt.Sprintf("Broker %s iniciou eleição para novo líder", msg.BrokerID), "warn")
		liderMu.Lock()
		statusEleicao = "EM_ELEICAO"
		liderAtual = "ELEGENDO..."
		liderMu.Unlock()

	case models.MsgCoordenador:
		addEvento("LIDER", fmt.Sprintf("Novo líder eleito: %s", msg.BrokerID), "info")
		liderMu.Lock()
		statusEleicao = "ESTAVEL"
		liderAtual = msg.BrokerID
		liderMu.Unlock()

	case models.MsgPeerList:
		for _, peer := range msg.Peers {
			estadoMu.Lock()
			_, ok := brokers[peer]
			if !ok {
				brokers[peer] = &BrokerStatus{
					ID:   obterBrokerID(peer),
					Addr: peer,
				}
				estadoMu.Unlock()
				log.Printf("[MONITOR] Novo broker descoberto via Líder: %s. Conectando...", peer)
				go conectarBroker(peer)
			} else {
				estadoMu.Unlock()
			}
		}

	case models.MsgSincDrone:
		if msg.Drone == nil {
			return
		}
		estadoMu.Lock()
		drones[msg.Drone.DroneID] = *msg.Drone
		estadoMu.Unlock()

	case models.MsgDroneDespachado:
		estadoMu.Lock()
		o, ok := ocorrencias[msg.OcorrenciaID]
		alreadyDespatched := ok && o.Status == "ANDAMENTO"
		if !alreadyDespatched {
			if !ok {
				o = OcorrenciaDetalhada{
					ID:          msg.OcorrenciaID,
					Status:      "ANDAMENTO",
					LamportTime: msg.LamportTime,
				}
			} else {
				o.Status = "ANDAMENTO"
			}
			ocorrencias[msg.OcorrenciaID] = o
		}
		estadoMu.Unlock()
		if !alreadyDespatched {
			addEvento("DESPACHO",
				fmt.Sprintf("Drone %s despachado para ocorrência %s (broker %s)",
					msg.DroneID, msg.OcorrenciaID, msg.BrokerID), "warn")
		}

	case models.MsgDronePerdido:
		estadoMu.Lock()
		d, ok := drones[msg.DroneID]
		alreadyAbatido := ok && d.Estado == models.DroneAbatido
		if !alreadyAbatido {
			if !ok {
				d = models.InfoDrone{
					DroneID: msg.DroneID,
					Estado:  models.DroneAbatido,
				}
			} else {
				d.Estado = models.DroneAbatido
			}
			drones[msg.DroneID] = d
		}
		estadoMu.Unlock()
		if !alreadyAbatido {
			addEvento("PERDA",
				fmt.Sprintf("Drone %s PERDIDO — %s", msg.DroneID, msg.Motivo), "danger")
		}

	case models.MsgDroneLiberado:
		estadoMu.Lock()
		d, ok := drones[msg.DroneID]
		alreadyAvailable := ok && d.Estado == models.DroneDisponivel
		if !alreadyAvailable {
			if !ok {
				d = models.InfoDrone{
					DroneID: msg.DroneID,
					Estado:  models.DroneDisponivel,
				}
			} else {
				d.Estado = models.DroneDisponivel
			}
			drones[msg.DroneID] = d
		}
		estadoMu.Unlock()
		if !alreadyAvailable {
			addEvento("LIBERADO", fmt.Sprintf("Drone %s disponível", msg.DroneID), "info")
		}

	case models.MsgMissaoConcluida:
		estadoMu.Lock()
		o, ok := ocorrencias[msg.OcorrenciaID]
		alreadyCompleted := ok && o.Status == "CONCLUIDA"
		if !alreadyCompleted {
			if !ok {
				o = OcorrenciaDetalhada{
					ID:          msg.OcorrenciaID,
					Status:      "CONCLUIDA",
					LamportTime: msg.LamportTime,
				}
			} else {
				o.Status = "CONCLUIDA"
			}
			ocorrencias[msg.OcorrenciaID] = o
		}
		estadoMu.Unlock()
		if !alreadyCompleted {
			addEvento("MISSAO",
				fmt.Sprintf("Missão concluída: drone %s liberou %s", msg.DroneID, msg.OcorrenciaID), "info")
		}

	case models.MsgRequisicaoDrone:
		if msg.Ocorrencia != nil {
			estadoMu.Lock()
			_, exists := ocorrencias[msg.Ocorrencia.ID]
			if !exists {
				ocorrencias[msg.Ocorrencia.ID] = OcorrenciaDetalhada{
					ID:          msg.Ocorrencia.ID,
					Tipo:        msg.Ocorrencia.Tipo,
					Criticidade: msg.Ocorrencia.Criticidade.String(),
					Status:      "ESPERA",
					Timestamp:   msg.Ocorrencia.Timestamp,
					LamportTime: msg.Ocorrencia.LamportTime,
				}
			}
			estadoMu.Unlock()
			if !exists {
				addEvento("REQUISICAO",
					fmt.Sprintf("Ocorrência %s [%s] em %s", msg.Ocorrencia.ID,
						msg.Ocorrencia.Criticidade, msg.Ocorrencia.SetorOrigem), "warn")
			}
		}

	case models.MsgFailover:
		estadoMu.Lock()
		prevBroker, alreadyFailover := failovers[msg.SetorID]
		isNewFailover := !alreadyFailover || prevBroker != msg.BrokerID
		if isNewFailover {
			failovers[msg.SetorID] = msg.BrokerID
		}
		estadoMu.Unlock()
		if isNewFailover {
			addEvento("FAILOVER", fmt.Sprintf("Broker %s assumiu setor %s (failover)", msg.BrokerID, msg.SetorID), "danger")
		}

	case models.MsgFailoverRecuperado:
		estadoMu.Lock()
		_, isFailoverActive := failovers[msg.SetorID]
		if isFailoverActive {
			delete(failovers, msg.SetorID)
		}
		estadoMu.Unlock()
		if isFailoverActive {
			addEvento("RECUPERACAO", fmt.Sprintf("Broker %s recuperou setor %s", msg.BrokerID, msg.SetorID), "info")
		}
	}
}

// ── HTTP + WebSocket ──────────────────────────────────────────────────────────

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, _, err := wsUpgrade(w, r)
	if err != nil {
		return
	}
	c := &wsClient{conn: conn}
	hub.add(c)
	defer hub.remove(c)

	// Envia estado inicial imediatamente
	hub.broadcast(snapshot())

	// Lê frames (ignora — monitor é só leitura)
	for {
		if _, err := wsLer(conn); err != nil {
			return
		}
	}
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	brokersFlag := flag.String("brokers", "localhost:6000", "Endereços TCP dos brokers (vírgula)")
	porta := flag.String("porta", "8085", "Porta HTTP do dashboard")
	explorerPorta := flag.String("explorer-porta", "8086", "Porta HTTP do Blockchain Explorer")
	flag.Parse()
	_ = explorerPorta

	addrs := strings.Split(*brokersFlag, ",")
	var finalAddrs []string
	for _, addr := range addrs {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		finalAddrs = append(finalAddrs, addr)

		if strings.HasSuffix(addr, ":6008") {
			host, _, _ := net.SplitHostPort(addr)
			for p := 6007; p >= 6000; p-- {
				fallbackAddr := fmt.Sprintf("%s:%d", host, p)
				finalAddrs = append(finalAddrs, fallbackAddr)
			}
		}
	}

	for _, addr := range finalAddrs {
		estadoMu.Lock()
		if _, ok := brokers[addr]; !ok {
			brokers[addr] = &BrokerStatus{ID: obterBrokerID(addr), Addr: addr, Vivo: false}
			go conectarBroker(addr)
		}
		estadoMu.Unlock()
	}

	// Push de estado a cada 1s para todos os clientes WS
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			hub.broadcast(snapshot())
		}
	}()

	// Verifica brokers mortos por timeout de heartbeat
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			estadoMu.Lock()
			for _, b := range brokers {
				if b.Vivo && time.Since(b.UltimoHB) > 15*time.Second {
					b.Vivo = false
				}
			}
			estadoMu.Unlock()
		}
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, dashboardHTML)
	})
	http.Handle("/assets/", http.StripPrefix("/assets/", http.FileServer(http.Dir("./assets"))))
	http.HandleFunc("/ws", handleWS)
	http.HandleFunc("/api/estado", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(snapshot())
	})
	http.HandleFunc("/api/explorer/blocks", handleExplorerBlocks)
	http.HandleFunc("/api/explorer/mempool", handleExplorerMempool)
	http.HandleFunc("/api/explorer/balances", handleExplorerBalances)
	http.HandleFunc("/api/explorer/laudos", handleExplorerLaudos)
	http.HandleFunc("/api/explorer/payments", handleExplorerPayments)

	log.Printf("[MONITOR] Dashboard: http://localhost:%s", *porta)
	log.Printf("[MONITOR] Observando brokers: %s", *brokersFlag)
	if err := http.ListenAndServe(":"+*porta, nil); err != nil {
		log.Fatal(err)
	}
}

// ── Dashboard HTML ────────────────────────────────────────────────────────────

const dashboardHTML = `<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>HormuzNet — Centro de Controle</title>
<link href="https://fonts.googleapis.com/css2?family=Orbitron:wght@400;700;900&family=Share+Tech+Mono&display=swap" rel="stylesheet">
<style>
:root {
  --bg:       #05080d;
  --bg2:      #0b121a;
  --bg3:      #101a26;
  --border:   #1e3a4f;
  --green:    #00ff88;
  --green2:   #00cc6e;
  --green3:   #004422;
  --amber:    #ffcc00;
  --red:      #ff4444;
  --blue:     #00d4ff;
  --dim:      #345a70;
  --text:     #e0f2f7;
  --textdim:  #7fb89d;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

body {
  font-family: 'Share Tech Mono', monospace;
  background: var(--bg);
  color: var(--text);
  height: 100vh;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* ── HEADER ── */
header {
  background: var(--bg2);
  border-bottom: 1px solid var(--border);
  padding: 10px 20px;
  display: flex;
  align-items: center;
  gap: 20px;
  flex-shrink: 0;
  position: relative;
}
header::after {
  content: '';
  position: absolute;
  bottom: 0; left: 0; right: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, var(--green), transparent);
  opacity: 0.4;
}
.logo {
  font-family: 'Orbitron', sans-serif;
  font-size: 1.1rem;
  font-weight: 900;
  letter-spacing: .15em;
  color: var(--green);
  text-shadow: 0 0 20px rgba(0,232,122,.4);
}
.logo span { color: var(--textdim); font-weight: 400; }
.header-stats {
  display: flex;
  gap: 24px;
  margin-left: auto;
}
.hstat {
  display: flex;
  flex-direction: column;
  align-items: center;
  font-size: .65rem;
  color: var(--textdim);
  letter-spacing: .08em;
}
.hstat b {
  font-size: 1.3rem;
  color: var(--text);
  font-family: 'Orbitron', sans-serif;
  font-weight: 700;
}
.hstat b.green { color: var(--green); }
.hstat b.amber { color: var(--amber); }
.hstat b.red   { color: var(--red); }
.hstat b.blue  { color: var(--blue); }
.ws-dot {
  width: 8px; height: 8px;
  border-radius: 50%;
  background: var(--red);
  box-shadow: 0 0 6px var(--red);
  margin-left: auto;
  flex-shrink: 0;
}
.ws-dot.on { background: var(--green); box-shadow: 0 0 8px var(--green); }
.clock {
  font-family: 'Orbitron', sans-serif;
  font-size: .9rem;
  color: var(--green);
  letter-spacing: .1em;
  flex-shrink: 0;
}

/* ── LAYOUT PRINCIPAL ── */
.main-container {
  display: flex;
  flex-direction: column;
  flex: 1;
  overflow: hidden;
  position: relative;
}
#conteudo-tatico {
  display: grid;
  grid-template-columns: 320px 1fr 320px;
  grid-template-rows: 1fr 280px;
  gap: 1px;
  background: var(--border);
  flex: 1;
  overflow: hidden;
}
#conteudo-explorer {
  display: grid;
  grid-template-columns: 450px 1fr;
  gap: 1px;
  background: var(--border);
  flex: 1;
  overflow: hidden;
}
.table-balances {
  width: 100%;
  border-collapse: collapse;
}
.table-balances th {
  text-align: left;
  padding: 8px 10px;
  font-size: .7rem;
  color: var(--blue);
  border-bottom: 2px solid var(--border);
}
.table-balances td {
  padding: 10px;
  font-size: .75rem;
  border-bottom: 1px solid rgba(27,50,71,0.5);
}
.balance-val {
  font-family: 'Orbitron', sans-serif;
  font-weight: bold;
  color: var(--green);
}
.block-card {
  background: var(--bg2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 12px;
  margin-bottom: 10px;
  transition: all 0.2s;
}
.block-card:hover {
  border-color: var(--blue);
  box-shadow: 0 0 10px rgba(0,204,255,0.2);
}
.badge {
  display: inline-block;
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 0.6rem;
  font-weight: bold;
  margin-right: 6px;
}
.badge.mint { background: rgba(0,255,102,0.15); color: var(--green); }
.badge.reg { background: rgba(0,204,255,0.15); color: var(--blue); }
.badge.transfer { background: rgba(255,187,0,0.15); color: var(--amber); }
.badge.vessel { background: rgba(204,102,255,0.15); color: var(--purple); }
.tx-item {
  border-bottom: 1px solid rgba(27,50,71,0.3);
  padding: 6px 0;
  font-size: 0.72rem;
}
.tx-item:last-child { border-bottom: none; }

/* ── TAB BUTTONS ── */
.tabs-container {
  display: flex;
  gap: 10px;
  margin-left: 20px;
}
.tab-btn {
  background: transparent;
  border: 1px solid var(--border);
  color: var(--textdim);
  padding: 6px 14px;
  font-family: 'Orbitron', sans-serif;
  font-size: 0.75rem;
  font-weight: bold;
  cursor: pointer;
  border-radius: 4px;
  transition: all 0.2s ease-in-out;
}
.tab-btn:hover {
  color: var(--text);
  border-color: var(--blue);
  box-shadow: 0 0 8px rgba(0, 194, 255, 0.3);
}
.tab-btn.active {
  color: #05080d;
  background: var(--green);
  border-color: var(--green);
  box-shadow: 0 0 10px rgba(0, 255, 136, 0.4);
}
.panel-bottom {
  grid-column: 1 / span 3;
}
.panel {
  background: var(--bg);
  overflow: hidden;
  display: flex;
  flex-direction: column;
}
.panel-title {
  font-family: 'Orbitron', sans-serif;
  font-size: .85rem;
  font-weight: 700;
  letter-spacing: .2em;
  color: var(--green2);
  padding: 10px 14px 8px;
  border-bottom: 1px solid var(--border);
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-shrink: 0;
}
.panel-title .cnt {
  background: var(--green3);
  color: var(--green);
  padding: 1px 7px;
  border-radius: 10px;
  font-size: .6rem;
}
.panel-body { flex: 1; overflow-y: auto; padding: 10px; }
.panel-body::-webkit-scrollbar { width: 3px; }
.panel-body::-webkit-scrollbar-thumb { background: var(--border); }

/* ── MAPA ── */
.map-wrap { 
  flex: 1; position: relative; overflow: hidden; 
  background: url('/assets/map.png') center center / cover no-repeat;
}
canvas#mapa {
  width: 100%; height: 100%;
  display: block;
}
.map-legend {
  position: absolute;
  bottom: 10px; right: 10px;
  background: rgba(7,11,18,.85);
  border: 1px solid var(--border);
  padding: 8px 12px;
  font-size: .8rem;
  line-height: 1.8;
}
.leg { display: flex; align-items: center; gap: 6px; }
.leg-dot { width: 8px; height: 8px; border-radius: 50%; }

/* ── DRONES ── */
.drone-card {
  background: var(--bg2);
  border: 1px solid var(--border);
  border-left: 3px solid var(--dim);
  border-radius: 4px;
  padding: 8px 10px;
  margin-bottom: 6px;
  font-size: .85rem;
  transition: border-color .2s;
}
.drone-card.DISPONIVEL  { border-left-color: var(--green); }
.drone-card.DESPACHADO  { border-left-color: var(--amber); }
.drone-card.EM_MISSAO   { border-left-color: var(--blue);  }
.drone-card.RETORNANDO  { border-left-color: var(--textdim); }
.drone-card.ABATIDO { border-left-color: var(--red); opacity:.6; }
.drone-card.REALOCANDO  { border-left-color: var(--amber); }

.dc-top { display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px; }
.dc-id  { color: var(--text); font-weight: bold; letter-spacing: .04em; }
.dc-est {
  font-size: .6rem;
  padding: 1px 7px;
  border-radius: 3px;
  background: var(--bg3);
  letter-spacing: .06em;
}
.dc-est.DISPONIVEL  { color: var(--green); }
.dc-est.DESPACHADO  { color: var(--amber); }
.dc-est.EM_MISSAO   { color: var(--blue);  }
.dc-est.RETORNANDO  { color: var(--textdim); }
.dc-est.ABATIDO { color: var(--red); }
.dc-est.REALOCANDO  { color: var(--amber); }

.dc-info { color: var(--textdim); font-size: .66rem; display: flex; gap: 12px; }

/* ── BROKERS ── */
.broker-card {
  background: var(--bg2);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 8px 10px;
  margin-bottom: 6px;
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: .7rem;
}
.br-led {
  width: 9px; height: 9px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--red);
  box-shadow: 0 0 5px var(--red);
}
.br-led.on { background: var(--green); box-shadow: 0 0 8px var(--green); }
.br-info { flex: 1; }
.br-id { color: var(--text); font-weight: bold; }
.br-addr { color: var(--textdim); font-size: .62rem; }
.br-hb { color: var(--textdim); font-size: .6rem; margin-left: auto; }

/* ── LOG ── */
.log-wrap { flex: 1; overflow-y: auto; padding: 8px; }
.log-wrap::-webkit-scrollbar { width: 3px; }
.log-wrap::-webkit-scrollbar-thumb { background: var(--border); }
.log-item {
  display: flex;
  gap: 8px;
  padding: 4px 0;
  border-bottom: 1px solid rgba(26,48,64,.5);
  font-size: .64rem;
  line-height: 1.4;
}
.log-hora { color: var(--textdim); flex-shrink: 0; }
.log-tipo {
  flex-shrink: 0;
  width: 70px;
  font-weight: bold;
  font-size: .6rem;
}
.log-tipo.info   { color: var(--green2); }
.log-tipo.warn   { color: var(--amber); }
.log-tipo.danger { color: var(--red); }
.log-msg { color: var(--text); }

/* ── TABELA DE FILA ── */
.table-wrap { flex: 1; overflow-y: auto; padding: 0; }
.fila-pedidos { width: 100%; border-collapse: collapse; font-family: 'Share Tech Mono', monospace; }
.fila-pedidos th { 
  position: sticky; top: 0; background: var(--bg3); 
  text-align: left; padding: 10px; font-size: .7rem; 
  color: var(--green); border-bottom: 2px solid var(--border);
}
.fila-pedidos td { padding: 8px 10px; font-size: .75rem; border-bottom: 1px solid rgba(26,48,64,.3); }
.st-espera { color: var(--amber); }
.st-andamento { color: var(--blue); font-weight: bold; }
.st-concluida { color: var(--green); opacity: .8; }

/* ── SCAN LINE EFFECT ── */
body::before {
  content: '';
  position: fixed;
  inset: 0;
  background: repeating-linear-gradient(
    0deg,
    transparent,
    transparent 2px,
    rgba(0,0,0,.06) 2px,
    rgba(0,0,0,.06) 4px
  );
  pointer-events: none;
  z-index: 9999;
}
@keyframes pulse {
  0% { opacity: 0.3; text-shadow: 0 0 5px var(--amber); }
  50% { opacity: 1; text-shadow: 0 0 15px var(--amber); }
  100% { opacity: 0.3; text-shadow: 0 0 5px var(--amber); }
}
.pulse {
  animation: pulse 1.5s infinite;
}
.broker-card.leader {
  border-color: #00c2ff !important;
  box-shadow: 0 0 10px rgba(0, 194, 255, 0.4), inset 0 0 4px rgba(0, 232, 122, 0.2) !important;
}
</style>
</head>
<body>

<header>
  <div class="logo">HORMUZNET <span>// CENTRO DE CONTROLE</span></div>
  <div class="tabs-container">
    <button id="btn-tab-tatico" class="tab-btn active" onclick="showTab('tatico')">📊 TÁTICO</button>
    <button id="btn-tab-explorer" class="tab-btn" onclick="showTab('explorer')">🔗 BLOCKCHAIN</button>
    <button id="btn-tab-laudos" class="tab-btn" onclick="showTab('laudos')">📋 LAUDOS</button>
    <button id="btn-tab-pagamentos" class="tab-btn" onclick="showTab('pagamentos')">💳 PAGAMENTOS</button>
  </div>
  <div class="header-stats">
    <div class="hstat" style="border-right: 1px solid var(--border); padding-right: 15px; margin-right: 5px;">
      <span style="font-size: .6rem; color: var(--textdim); letter-spacing: .08em;">LÍDER DA REDE</span>
      <b class="green" id="h-lider">B9</b>
      <span id="h-lider-status" style="font-size: 0.55rem; color: var(--green); font-weight: bold;">ESTÁVEL</span>
    </div>
    <div class="hstat"><b class="green" id="h-disp">0</b>DISPONÍVEIS</div>
    <div class="hstat"><b class="amber" id="h-miss">0</b>EM MISSÃO</div>
    <div class="hstat"><b class="blue"  id="h-ret">0</b>RETORNANDO</div>
    <div class="hstat"><b class="red"   id="h-perd">0</b>PERDIDOS</div>
    <div class="hstat"><b id="h-total">0</b>TOTAL DRONES</div>
    <div style="width: 20px; border-left: 1px solid var(--border); margin: 0 10px;"></div>
    <div class="hstat"><b class="amber" id="h-oc-esp">0</b>EM ESPERA</div>
    <div class="hstat"><b class="blue" id="h-oc-and">0</b>EM ANDAMENTO</div>
    <div class="hstat"><b class="green" id="h-oc-con">0</b>CONCLUÍDAS</div>
  </div>
  <div class="clock" id="clock">--:--:--</div>
  <div class="ws-dot" id="wsdot"></div>
</header>

<div class="main-container">
  <div id="conteudo-tatico">

  <!-- Coluna esquerda: drones -->
  <div class="panel">
    <div class="panel-title">
      UNIDADES AÉREAS
      <span class="cnt" id="cnt-drones">0</span>
    </div>
    <div class="panel-body" id="lista-drones">
      <div style="color:var(--textdim);font-size:.7rem;text-align:center;margin-top:40px">
        Aguardando conexão...
      </div>
    </div>
  </div>

  <!-- Centro: mapa tático -->
  <div class="panel">
    <div class="panel-title">MAPA TÁTICO — VISÃO CARTESIANA</div>
    <div class="map-wrap">
      <canvas id="mapa"></canvas>
      <div class="map-legend">
        <div class="leg"><div class="leg-dot" style="background:var(--green)"></div>DISPONÍVEL</div>
        <div class="leg"><div class="leg-dot" style="background:var(--amber)"></div>DESPACHADO</div>
        <div class="leg"><div class="leg-dot" style="background:var(--blue)"></div>EM MISSÃO</div>
        <div class="leg"><div class="leg-dot" style="background:var(--textdim)"></div>RETORNANDO</div>
        <div class="leg"><div class="leg-dot" style="background:var(--red)"></div>PERDIDO</div>
      </div>
    </div>
  </div>

  <!-- Coluna direita: brokers + log -->
  <div class="panel">
    <div class="panel-title">
      BROKERS
      <span class="cnt" id="cnt-brokers">0</span>
    </div>
    <div style="padding:8px;flex-shrink:0;max-height:240px;overflow-y:auto" id="lista-brokers"></div>
    <div class="panel-title" style="margin-top:4px">LOG DE EVENTOS</div>
    <div class="log-wrap" id="log-eventos"></div>
  </div>

  <!-- Rodapé: Fila de Pedidos -->
  <div class="panel panel-bottom">
    <div class="panel-title">
      FILA DE PEDIDOS EM TEMPO REAL
      <span class="cnt" id="cnt-ocorrencias">0</span>
    </div>
    <div class="table-wrap">
      <table class="fila-pedidos">
        <thead>
          <tr>
            <th>LAMPORT</th>
            <th>ID DO PEDIDO</th>
            <th>TIPO</th>
            <th>CRITICIDADE</th>
            <th>STATUS ATUAL</th>
          </tr>
        </thead>
        <tbody id="corpo-fila">
          <!-- Dinâmico -->
        </tbody>
      </table>
    </div>
  </div>

  </div> <!-- Fim de conteudo-tatico -->

  <div id="conteudo-explorer" style="display:none">
    <!-- Coluna esquerda: balances e mempool -->
    <div style="display:flex; flex-direction:column; gap:1px; background:var(--border); overflow:hidden;">
      <!-- Saldos -->
      <div class="panel" style="flex:1; display:flex; flex-direction:column; overflow:hidden;">
        <div class="panel-title">SALDOS DAS EMPRESAS (ELIS)</div>
        <div class="panel-body">
          <table class="table-balances">
            <thead>
              <tr>
                <th>EMPRESA</th>
                <th>ENDEREÇO</th>
                <th>SALDO ELIS</th>
                <th>NAVIOS</th>
              </tr>
            </thead>
            <tbody id="balances-body">
              <tr><td colspan="4" style="text-align:center;color:var(--textdim)">Carregando saldos...</td></tr>
            </tbody>
          </table>
        </div>
      </div>
      <!-- Mempool -->
      <div class="panel" style="height:350px; display:flex; flex-direction:column; overflow:hidden; border-top:1px solid var(--border)">
        <div class="panel-title">MEMPOOL (TRANSAÇÕES PENDENTES)</div>
        <div class="panel-body" id="mempool-body" style="overflow-y:auto">
          <div style="text-align:center;color:var(--textdim);font-size:.75rem">Nenhuma transação pendente.</div>
        </div>
      </div>
    </div>
    <!-- Coluna direita: blocos -->
    <div class="panel" style="display:flex; flex-direction:column; overflow:hidden;">
      <div class="panel-title">HISTÓRICO DE BLOCOS (PoA CONSENSUS)</div>
      <div class="panel-body" id="blocks-body" style="overflow-y:auto">
        <div style="text-align:center;color:var(--textdim)">Carregando blocos...</div>
      </div>
    </div>
  </div>

  <!-- ABA LAUDOS -->
  <div id="conteudo-laudos" style="display:none; flex:1; overflow:hidden; flex-direction:column;">
    <div style="padding:10px 14px; background:var(--bg2); border-bottom:1px solid var(--border); display:flex; gap:12px; align-items:center; flex-shrink:0">
      <span style="font-size:.75rem; color:var(--textdim)">Filtrar por empresa (endereço 0x... para ver detalhes privados):</span>
      <input id="laudos-company-filter" type="text" placeholder="0x..." style="background:var(--bg3);border:1px solid var(--border);color:var(--text);padding:4px 10px;font-family:monospace;font-size:.75rem;border-radius:3px;width:260px;">
      <button onclick="updateLaudos()" style="background:var(--green3);border:1px solid var(--green);color:var(--green);padding:4px 12px;font-family:'Orbitron',sans-serif;font-size:.65rem;cursor:pointer;border-radius:3px;">FILTRAR</button>
      <span id="laudos-count" style="color:var(--textdim);font-size:.7rem;margin-left:auto">0 laudos</span>
    </div>
    <div style="flex:1;overflow-y:auto;padding:12px">
      <table style="width:100%;border-collapse:collapse;" id="laudos-table">
        <thead>
          <tr style="position:sticky;top:0;background:var(--bg3);">
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">OCORRÊNCIA</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">NAVIO</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">EMPRESA</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">DRONE</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">ESCOLTA (ELIS)</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">DRONE (ELIS)</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">TAXA BROKER</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">LAUDO</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--green);border-bottom:2px solid var(--border);">DATA</th>
          </tr>
        </thead>
        <tbody id="laudos-body">
          <tr><td colspan="9" style="text-align:center;color:var(--textdim);padding:30px">Nenhum laudo registrado ainda.</td></tr>
        </tbody>
      </table>
    </div>
  </div>

  <!-- ABA PAGAMENTOS -->
  <div id="conteudo-pagamentos" style="display:none; flex:1; overflow:hidden; flex-direction:column;">
    <div style="padding:10px 14px; background:var(--bg2); border-bottom:1px solid var(--border); display:flex; gap:12px; align-items:center; flex-shrink:0">
      <span style="font-size:.75rem; color:var(--textdim)">Filtrar por empresa (endereço 0x... para ver detalhes privados):</span>
      <input id="pag-company-filter" type="text" placeholder="0x..." style="background:var(--bg3);border:1px solid var(--border);color:var(--text);padding:4px 10px;font-family:monospace;font-size:.75rem;border-radius:3px;width:260px;">
      <button onclick="updatePagamentos()" style="background:var(--green3);border:1px solid var(--green);color:var(--green);padding:4px 12px;font-family:'Orbitron',sans-serif;font-size:.65rem;cursor:pointer;border-radius:3px;">FILTRAR</button>
      <span id="pag-count" style="color:var(--textdim);font-size:.7rem;margin-left:auto">0 transações</span>
    </div>
    <div style="flex:1;overflow-y:auto;padding:12px">
      <table style="width:100%;border-collapse:collapse;">
        <thead>
          <tr style="position:sticky;top:0;background:var(--bg3);">
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">TIPO</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">DE</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">PARA</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">VALOR (ELIS)</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">NAVIO</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">DESCRIÇÃO</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">BLOCO</th>
            <th style="text-align:left;padding:10px 8px;font-size:.7rem;color:var(--blue);border-bottom:2px solid var(--border);">DATA</th>
          </tr>
        </thead>
        <tbody id="pag-body">
          <tr><td colspan="8" style="text-align:center;color:var(--textdim);padding:30px">Nenhuma transação registrada ainda.</td></tr>
        </tbody>
      </table>
    </div>
  </div>

</div> <!-- Fim de main-container -->

<script>
// ── Estado ────────────────────────────────────────────────────────────────────
let estado = {drones: {}, brokers: [], eventos: [], failovers: {}};
let ws = null;
let currentTab = 'tatico';

function showTab(tab) {
  currentTab = tab;
  document.getElementById('btn-tab-tatico').className    = 'tab-btn' + (tab === 'tatico'    ? ' active' : '');
  document.getElementById('btn-tab-explorer').className  = 'tab-btn' + (tab === 'explorer'  ? ' active' : '');
  document.getElementById('btn-tab-laudos').className    = 'tab-btn' + (tab === 'laudos'    ? ' active' : '');
  document.getElementById('btn-tab-pagamentos').className= 'tab-btn' + (tab === 'pagamentos'? ' active' : '');

  document.getElementById('conteudo-tatico').style.display     = tab === 'tatico'    ? 'grid'  : 'none';
  document.getElementById('conteudo-explorer').style.display   = tab === 'explorer'  ? 'grid'  : 'none';
  document.getElementById('conteudo-laudos').style.display     = tab === 'laudos'    ? 'flex'  : 'none';
  document.getElementById('conteudo-pagamentos').style.display = tab === 'pagamentos'? 'flex'  : 'none';
  
  if (tab === 'explorer') updateExplorer();
  if (tab === 'laudos')   updateLaudos();
  if (tab === 'pagamentos') updatePagamentos();
}

function formatTime(isoStr) {
  if (!isoStr || isoStr.startsWith('0001-01-01')) return '--';
  try {
    const d = new Date(isoStr);
    if (isNaN(d.getTime())) return '--';
    return d.toLocaleTimeString('pt-BR');
  } catch (e) {
    return '--';
  }
}
const COR = {
  DISPONIVEL:'#00e87a', DESPACHADO:'#ffb800', EM_MISSAO:'#00c2ff',
  RETORNANDO:'#4a7060', ABATIDO:'#ff3b3b', REALOCANDO:'#ffb800'
};

// ── Relógio ───────────────────────────────────────────────────────────────────
setInterval(() => {
  document.getElementById('clock').textContent =
    new Date().toLocaleTimeString('pt-BR');
}, 1000);

// ── WebSocket ─────────────────────────────────────────────────────────────────
function conectar() {
  ws = new WebSocket('ws://' + location.host + '/ws');
  ws.onopen  = () => document.getElementById('wsdot').classList.add('on');
  ws.onclose = () => { document.getElementById('wsdot').classList.remove('on'); setTimeout(conectar, 2500); };
  ws.onmessage = e => {
    try { estado = JSON.parse(e.data); renderTudo(); } catch(_){}
  };
}
conectar();

// ── Render ────────────────────────────────────────────────────────────────────
function renderTudo() {
  renderDrones();
  renderBrokers();
  renderLog();
  renderMapa();
  renderOcorrencias();
  atualizarHeader();
}

function atualizarHeader() {
  const d = Object.values(estado.drones || {});
  const disp  = d.filter(x => x.estado === 'DISPONIVEL').length;
  const miss  = d.filter(x => x.estado === 'EM_MISSAO' || x.estado === 'DESPACHADO').length;
  const ret   = d.filter(x => x.estado === 'RETORNANDO').length;
  const perd  = d.filter(x => x.estado === 'ABATIDO').length;
  document.getElementById('h-disp').textContent  = disp;
  document.getElementById('h-miss').textContent  = miss;
  document.getElementById('h-ret').textContent   = ret;
  document.getElementById('h-perd').textContent  = perd;
  document.getElementById('h-total').textContent = d.length;

  const o = Object.values(estado.ocorrencias || {});
  const esp = o.filter(x => x.status === 'ESPERA').length;
  const and = o.filter(x => x.status === 'ANDAMENTO').length;
  const con = o.filter(x => x.status === 'CONCLUIDA').length;
  document.getElementById('h-oc-esp').textContent = esp;
  document.getElementById('h-oc-and').textContent = and;
  document.getElementById('h-oc-con').textContent = con;

  const hLider = document.getElementById('h-lider');
  const hLiderStatus = document.getElementById('h-lider-status');
  if (hLider && hLiderStatus) {
    if (estado.status_eleicao === 'EM_ELEICAO') {
      hLider.textContent = estado.lider_atual || 'ELEGENDO...';
      hLider.className = 'amber pulse';
      hLiderStatus.textContent = 'EM ELEIÇÃO';
      hLiderStatus.style.color = 'var(--amber)';
      hLiderStatus.className = 'pulse';
    } else {
      hLider.textContent = estado.lider_atual || 'B9';
      hLider.className = 'green';
      hLiderStatus.textContent = 'ESTÁVEL';
      hLiderStatus.style.color = 'var(--green)';
      hLiderStatus.className = '';
    }
  }
}

function renderOcorrencias() {
  const cont = document.getElementById('corpo-fila');
  const olist = Object.values(estado.ocorrencias || {})
    .sort((a,b) => b.id.localeCompare(a.id)) // Mais recentes primeiro
    .slice(0, 50);
  document.getElementById('cnt-ocorrencias').textContent = Object.keys(estado.ocorrencias).length;

  cont.innerHTML = olist.map(o => {
    const stClass = 'st-' + o.status.toLowerCase();
    const lamport = 'L-' + (o.lamport_time || 0);
    return '<tr>'
      + '<td style="font-weight:bold;color:#00e87a">' + lamport + '</td>'
      + '<td style="font-size:.65rem;color:var(--dim)">' + o.id + '</td>'
      + '<td>' + o.tipo.toUpperCase() + '</td>'
      + '<td style="color:' + (o.criticidade==='ALTA'?'var(--red)':'var(--textdim)') + '">' + o.criticidade + '</td>'
      + '<td class="' + stClass + '">' + o.status + '</td>'
      + '</tr>';
  }).join('');
}

function renderDrones() {
  const cont = document.getElementById('lista-drones');
  const dlist = Object.values(estado.drones || {}).sort((a, b) => {
    const idA = a.drone_id || '';
    const idB = b.drone_id || '';
    return idA.localeCompare(idB, undefined, { numeric: true, sensitivity: 'base' });
  });
  document.getElementById('cnt-drones').textContent = dlist.length;
  if (!dlist.length) return;

  cont.innerHTML = dlist.map(d => {
    const oc = d.ocorrencia_id ? '<span style="color:var(--blue)">▶ ' + d.ocorrencia_id.slice(-12) + '</span>' : '';
    const pos = d.posicao ? '(' + Math.round(d.posicao.x) + ',' + Math.round(d.posicao.y) + ')' : '--';
    return '<div class="drone-card ' + d.estado + '">'
      + '<div class="dc-top">'
      +   '<span class="dc-id">' + d.drone_id + '</span>'
      +   '<span class="dc-est ' + d.estado + '">' + d.estado + '</span>'
      + '</div>'
      + '<div class="dc-info">'
      +   '<span>📍 ' + pos + '</span>'
      + '</div>'
      + (oc ? '<div style="margin-top:3px;font-size:.62rem">' + oc + '</div>' : '')
      + '</div>';
  }).join('');
}

function renderBrokers() {
  const cont = document.getElementById('lista-brokers');
  const todosOsBrokers = [
    { id: 'B1', setor: 'Setor_Noroeste' },
    { id: 'B2', setor: 'Setor_Nordeste' },
    { id: 'B3', setor: 'Setor_Sudoeste' },
    { id: 'B4', setor: 'Setor_Sudeste' }
  ];

  cont.innerHTML = todosOsBrokers.map(eb => {
    const b = (estado.brokers || []).find(x => x.id === eb.id);
    const vivo = b ? b.vivo : false;
    const addr = b ? b.addr : 'Offline';
    const hb = b ? formatTime(b.ultimo_hb) : '--';
    
    const isLeader = estado.lider_atual === eb.id && estado.status_eleicao !== 'EM_ELEICAO';
    const cardClass = 'broker-card' + (isLeader ? ' leader' : '');
    const leaderLabel = isLeader ? ' <span style="color:#00c2ff;font-weight:bold;font-size:0.6rem;margin-left:4px">[LÍDER]</span>' : '';

    return '<div class="' + cardClass + '" style="' + (!vivo ? 'opacity: 0.6;' : '') + '">'
      + '<div class="br-led' + (vivo ? ' on' : '') + '"></div>'
      + '<div class="br-info">'
      +   '<div class="br-id">' + eb.id + leaderLabel + ' <span style="font-size:0.6rem;color:var(--textdim)">(' + eb.setor.split('_')[1] + ')</span></div>'
      +   '<div class="br-addr">' + addr + '</div>'
      + '</div>'
      + '<div class="br-hb">' + hb + '</div>'
      + '</div>';
  }).join('');

  const ativos = (estado.brokers || []).filter(x => x.vivo).length;
  document.getElementById('cnt-brokers').textContent = ativos + '/' + todosOsBrokers.length;
}

function renderLog() {
  const cont = document.getElementById('log-eventos');
  const evs = (estado.eventos || []).slice().reverse().slice(0, 40);
  cont.innerHTML = evs.map(e => {
    const hora = formatTime(e.timestamp);
    return '<div class="log-item">'
      + '<span class="log-hora">' + hora + '</span>'
      + '<span class="log-tipo ' + e.nivel + '">' + e.tipo + '</span>'
      + '<span class="log-msg">' + e.mensagem + '</span>'
      + '</div>';
  }).join('');
}

// ── Mapa Tático ───────────────────────────────────────────────────────────────
function renderMapa() {
  const canvas = document.getElementById('mapa');
  const wrap = canvas.parentElement;
  canvas.width  = wrap.clientWidth;
  canvas.height = wrap.clientHeight;
  const ctx = canvas.getContext('2d');
  const W = canvas.width, H = canvas.height;

  // Limpa o fundo para mostrar a imagem
  ctx.clearRect(0, 0, W, H);

  // Grade
  ctx.strokeStyle = 'rgba(26,48,64,.5)';
  ctx.lineWidth = 1;
  const step = 60;
  for (let x = 0; x < W; x += step) { ctx.beginPath(); ctx.moveTo(x,0); ctx.lineTo(x,H); ctx.stroke(); }
  for (let y = 0; y < H; y += step) { ctx.beginPath(); ctx.moveTo(0,y); ctx.lineTo(W,y); ctx.stroke(); }

  // Eixos
  ctx.strokeStyle = 'rgba(0,232,122,.15)';
  ctx.lineWidth = 1;
  ctx.beginPath(); ctx.moveTo(W/2,0); ctx.lineTo(W/2,H); ctx.stroke();
  ctx.beginPath(); ctx.moveTo(0,H/2); ctx.lineTo(W,H/2); ctx.stroke();

  const setorParaBroker = {
    'Setor_Noroeste': 'B1',
    'Setor_Nordeste': 'B2',
    'Setor_Sudoeste': 'B3',
    'Setor_Sudeste': 'B4'
  };

  // Zonas dos Brokers (Grade 2x2)
  const drawSec = (c, x, y, w, h, setorName) => {
    let fill = c;
    let stroke = c.replace('0.15', '0.5');
    const failoverBroker = estado.failovers ? estado.failovers[setorName] : null;
    if (failoverBroker) {
      fill = 'rgba(255, 68, 68, 0.2)'; // Highlighted red/orange for failover
      stroke = 'rgba(255, 68, 68, 0.7)';
    } else {
      const bId = setorParaBroker[setorName];
      const broker = (estado.brokers || []).find(b => b.id === bId);
      const vivo = broker ? broker.vivo : false;
      if (!vivo) {
        fill = 'rgba(30, 30, 30, 0.55)'; // Grayed out/darker for offline
        stroke = 'rgba(255, 68, 68, 0.3)'; // Dim red stroke for offline
      }
    }
    ctx.fillStyle = fill; ctx.fillRect(x, y, w, h);
    ctx.strokeStyle = stroke; ctx.strokeRect(x, y, w, h);
  };
  const cw = W/2, ch = H/2;
  drawSec('rgba(0, 232, 122, 0.15)', 0, 0, cw, ch, 'Setor_Noroeste'); // NW
  drawSec('rgba(255, 184, 0, 0.15)', cw, 0, cw, ch, 'Setor_Nordeste'); // NE
  drawSec('rgba(255, 59, 59, 0.15)', 0, ch, cw, ch, 'Setor_Sudoeste'); // SW
  drawSec('rgba(0, 194, 255, 0.15)', cw, ch, cw, ch, 'Setor_Sudeste'); // SE

  const getLabel = (defaultText, setorName) => {
    const failoverBroker = estado.failovers ? estado.failovers[setorName] : null;
    if (failoverBroker) {
      return { text: defaultText + ' (FAILOVER: ' + failoverBroker + ')', color: 'rgba(255, 68, 68, 0.95)', font: 'bold 11px Orbitron' };
    }
    const bId = setorParaBroker[setorName];
    const broker = (estado.brokers || []).find(b => b.id === bId);
    const vivo = broker ? broker.vivo : false;
    if (!vivo) {
      return { text: defaultText + ' (INATIVO)', color: 'rgba(255, 68, 68, 0.65)', font: 'bold 10px Orbitron' };
    }
    return { text: defaultText, color: 'rgba(255, 255, 255, 0.85)', font: 'bold 11px Orbitron' };
  };

  const drawLabel = (regionName, defaultText, setorName, tx, ty) => {
    const lbl = getLabel(defaultText, setorName);
    
    // Draw Region Name (e.g. NOROESTE)
    ctx.fillStyle = 'rgba(255, 255, 255, 0.45)';
    ctx.font = 'bold 12px Orbitron';
    ctx.fillText(regionName, tx, ty - 10);

    // Draw Broker status below it
    ctx.fillStyle = lbl.color;
    ctx.font = lbl.font;
    ctx.fillText(lbl.text, tx, ty + 10);
  };

  ctx.textAlign = 'center';
  drawLabel('NOROESTE', 'B1: NOROESTE', 'Setor_Noroeste', cw/2, ch/2);
  drawLabel('NORDESTE', 'B2: NORDESTE', 'Setor_Nordeste', cw*1.5, ch/2);
  drawLabel('SUDOESTE', 'B3: SUDOESTE', 'Setor_Sudoeste', cw/2, ch*1.5);
  drawLabel('SUDESTE', 'B4: SUDESTE', 'Setor_Sudeste', cw*1.5, ch*1.5);

  const dlist = Object.values(estado.drones || {});
  if (!dlist.length) {
    ctx.fillStyle = 'rgba(74,112,96,.5)';
    ctx.font = '14px Share Tech Mono';
    ctx.textAlign = 'center';
    ctx.fillText('SEM DRONES REGISTRADOS', W/2, H/2);
    return;
  }

  // Escala fixa para grade 3x3 (0 a 1000 unidades)
  const xMin = 0, xMax = 1000;
  const yMin = 0, yMax = 1000;
  const scaleX = (W - 40) / (xMax - xMin);
  const scaleY = (H - 40) / (yMax - yMin);
  const scale  = Math.min(scaleX, scaleY);
  const offX = (W - (xMax - xMin) * scale) / 2;
  const offY = (H - (yMax - yMin) * scale) / 2;

  const toScreen = (x, y) => ({ sx: x * scale + offX, sy: H - (y * scale + offY) });



  // Drones
  dlist.forEach(d => {
    if (!d.posicao) return;
    const {sx, sy} = toScreen(d.posicao.x, d.posicao.y);
    const cor = COR[d.estado] || '#4a7060';

    // Aura para drones em missão
    if (d.estado === 'EM_MISSAO' || d.estado === 'DESPACHADO') {
      const grad = ctx.createRadialGradient(sx, sy, 2, sx, sy, 18);
      grad.addColorStop(0, cor + '44');
      grad.addColorStop(1, 'transparent');
      ctx.beginPath(); ctx.arc(sx, sy, 18, 0, Math.PI*2);
      ctx.fillStyle = grad; ctx.fill();
    }

    // Ponto do drone
    ctx.beginPath();
    ctx.arc(sx, sy, 5, 0, Math.PI * 2);
    ctx.fillStyle = cor;
    ctx.shadowColor = cor;
    ctx.shadowBlur = 8;
    ctx.fill();
    ctx.shadowBlur = 0;

    // Label
    ctx.fillStyle = cor;
    ctx.font = 'bold 10px Share Tech Mono';
    ctx.textAlign = 'center';
    ctx.fillText(d.drone_id.split('_').pop(), sx, sy - 10);
  });
}

window.addEventListener('resize', renderMapa);

// ── Polling do Explorer ──
async function updateExplorer() {
  if (currentTab !== 'explorer') return;
  try {
    const rBlocks = await fetch('/api/explorer/blocks');
    if (!rBlocks.ok) return;
    const blocks = await rBlocks.json();
    
    const rMempool = await fetch('/api/explorer/mempool');
    if (!rMempool.ok) return;
    const mempool = await rMempool.json();

    const rBalances = await fetch('/api/explorer/balances');
    if (!rBalances.ok) return;
    const balances = await rBalances.json();

    // Render Balances
    const balancesBody = document.getElementById('balances-body');
    if (balances.length === 0) {
      balancesBody.innerHTML = '<tr><td colspan="4" style="text-align:center;color:var(--textdim)">Nenhuma empresa registrada.</td></tr>';
    } else {
      balances.sort((a,b) => b.balance - a.balance);
      balancesBody.innerHTML = balances.map(c => {
        const vesselsStr = c.vessels && c.vessels.length > 0 ? c.vessels.join(', ') : 'Nenhum';
        return '<tr>' +
          '<td style="font-weight:bold;color:var(--blue)">' + c.name + '</td>' +
          '<td style="font-size:.65rem;color:var(--textdim);font-family:monospace">' + c.address.slice(0, 16) + '...</td>' +
          '<td class="balance-val">' + c.balance.toFixed(2) + ' ELIS</td>' +
          '<td style="font-size:.68rem;color:var(--textdim)">' + vesselsStr + '</td>' +
          '</tr>';
      }).join('');
    }

    // Render Mempool
    const mempoolBody = document.getElementById('mempool-body');
    if (mempool.length === 0) {
      mempoolBody.innerHTML = '<div style="text-align:center;color:var(--textdim);font-size:.75rem">Nenhuma transação pendente.</div>';
    } else {
      mempoolBody.innerHTML = mempool.map(tx => {
        let badge = '';
        let details = '';
        if (tx.type === 'MINT') {
          badge = '<span class="badge mint">MINT</span>';
          details = 'Mint de ' + tx.amount + ' ELIS para ' + tx.to.slice(0, 8);
        } else if (tx.type === 'REGISTER') {
          badge = '<span class="badge reg">REGISTER</span>';
          details = 'Registro de empresa: ' + tx.payload;
        } else if (tx.type === 'TRANSFER') {
          badge = '<span class="badge transfer">TRANSFER</span>';
          details = tx.from.slice(0,8) + ' envia ' + tx.amount + ' ELIS para ' + tx.to.slice(0,8) + (tx.payload ? ' ('+tx.payload+')' : '');
        } else if (tx.type === 'VESSEL_REG') {
          badge = '<span class="badge vessel">VESSEL</span>';
          details = 'Registro de Navio: ' + tx.vessel_id + ' por ' + tx.from.slice(0,8);
        }
        return '<div style="background:var(--bg3);border:1px solid var(--border);border-radius:4px;padding:8px;margin-bottom:8px">' +
          '<div style="display:flex;justify-content:space-between;margin-bottom:5px"><b>ID: ' + tx.id.slice(0,12) + '...</b>' + badge + '</div>' +
          '<div style="color:var(--textdim);font-size:.65rem">' + details + '</div>' +
          '</div>';
      }).join('');
    }

    // Render Blocks
    const blocksBody = document.getElementById('blocks-body');
    blocksBody.innerHTML = blocks.slice().reverse().map(b => {
      let txsHTML = '';
      if (b.transactions && b.transactions.length > 0) {
        txsHTML = b.transactions.map(tx => {
          let typeStr = 'TRANSFER';
          let badgeClass = 'transfer';
          let desc = '';
          if (tx.type === 'MINT') { typeStr = 'MINT'; badgeClass = 'mint'; desc = tx.amount + ' ELIS -> ' + tx.to.slice(0, 8); }
          else if (tx.type === 'REGISTER') { typeStr = 'REGISTER'; badgeClass = 'reg'; desc = 'Empresa ' + tx.payload; }
          else if (tx.type === 'TRANSFER') { typeStr = 'TRANSFER'; badgeClass = 'transfer'; desc = tx.from.slice(0, 8) + ' enviou ' + tx.amount + ' ELIS para ' + tx.to.slice(0, 8) + (tx.payload ? ' ('+tx.payload+')' : ''); }
          else if (tx.type === 'VESSEL_REG') { typeStr = 'VESSEL'; badgeClass = 'vessel'; desc = 'Navio ' + tx.vessel_id; }
          
          return '<div class="tx-item">' +
            '<span class="badge ' + badgeClass + '">' + typeStr + '</span> ' + desc + '<br/>' +
            '<span style="font-size:.6rem;color:var(--textdim)">TXID: ' + tx.id + '</span>' +
            '</div>';
        }).join('');
      } else {
        txsHTML = '<div style="color:var(--textdim);font-size:.65rem">Nenhuma transação no bloco.</div>';
      }

      const votesCount = b.signature ? b.signature.split(';').filter(x => x).length : 0;
      const consensusLabel = b.index === 0 ? 'GENESIS BLOCK' : votesCount + ' Assinaturas Validadoras (PoA)';

      return '<div class="block-card">' +
        '<div style="display:flex;justify-content:space-between;margin-bottom:8px">' +
          '<span style="font-weight:bold;color:var(--blue)">BLOCO #' + b.index + '</span>' +
          '<span style="font-size:.7rem;color:var(--textdim)">' + formatTime(b.timestamp) + '</span>' +
        '</div>' +
        '<div style="font-size:.65rem;color:var(--textdim)">HASH: ' + b.hash + '</div>' +
        '<div style="font-size:.65rem;color:var(--textdim);margin-bottom:8px">PREV: ' + b.prev_hash + '</div>' +
        '<div style="margin:10px 0 5px 0;font-weight:bold;font-size:.72rem;color:var(--blue)">TRANSAÇÕES:</div>' +
        '<div style="background:rgba(0,0,0,0.2);padding:8px;border-radius:4px;margin-bottom:8px">' + txsHTML + '</div>' +
        '<div style="font-size:.7rem;color:var(--green)">✓ ' + consensusLabel + '</div>' +
        '</div>';
    }).join('');

  } catch (err) {
    console.error("Erro ao atualizar dados do explorer: ", err);
  }
}

// ── Laudos ────────────────────────────────────────────────────────────────────
async function updateLaudos() {
  const companyAddr = document.getElementById('laudos-company-filter').value.trim();
  let url = '/api/explorer/laudos';
  if (companyAddr) url += '?company=' + encodeURIComponent(companyAddr);
  try {
    const resp = await fetch(url);
    if (!resp.ok) { console.error('Laudos fetch error'); return; }
    const laudos = await resp.json();
    document.getElementById('laudos-count').textContent = (laudos ? laudos.length : 0) + ' laudo(s)';
    const tbody = document.getElementById('laudos-body');
    if (!laudos || laudos.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8" style="text-align:center;color:var(--textdim);padding:30px">Nenhum laudo registrado ainda.</td></tr>';
      return;
    }
    tbody.innerHTML = laudos.slice().reverse().map(l => {
      const isPrivate = l.payload && l.payload.startsWith('[CONFIDENCIAL');
      const payloadHtml = isPrivate
        ? '<span style="color:var(--red);font-size:.65rem;cursor:pointer;text-decoration:underline;" onclick="revelarLaudo(\'' + l.occurrence_id + '\')">🔒 CONFIDENCIAL (clique)</span>'
        : '<span style="color:var(--green);font-size:.65rem" title="' + l.payload + '">' + (l.payload ? l.payload.slice(0,60) + (l.payload.length > 60 ? '...' : '') : '-') + '</span>';
      const escoltaStr = l.escort_amount ? l.escort_amount.toFixed(2) + ' ELIS' : '-';
      const droneStr   = l.drone_fee     ? l.drone_fee.toFixed(2) + ' ELIS' : '-';
      const taxaStr    = l.broker_fee    ? '<span style="color:var(--amber)">' + l.broker_fee.toFixed(2) + ' ELIS</span>' : '-';
      return '<tr style="border-bottom:1px solid rgba(30,58,79,.4);">' +
        '<td style="padding:8px;font-size:.68rem;font-family:monospace;color:var(--textdim)" title="' + l.occurrence_id + '">' + (l.occurrence_id ? l.occurrence_id.slice(0,18)+'...' : '-') + '</td>' +
        '<td style="padding:8px;font-size:.72rem;color:var(--blue)">' + (l.vessel_id || '-') + '</td>' +
        '<td style="padding:8px;font-size:.65rem;font-family:monospace;color:var(--textdim)" title="' + l.company_addr + '">' + (l.company_addr ? l.company_addr.slice(0,14)+'...' : '-') + '</td>' +
        '<td style="padding:8px;font-size:.72rem;color:var(--green)">' + (l.drone_id || '-') + '</td>' +
        '<td style="padding:8px;font-size:.72rem;font-family:\'Orbitron\',sans-serif;font-weight:bold;color:var(--amber)">' + escoltaStr + '</td>' +
        '<td style="padding:8px;font-size:.72rem;font-family:\'Orbitron\',sans-serif;font-weight:bold;color:var(--purple)">' + droneStr + '</td>' +
        '<td style="padding:8px;font-size:.72rem">' + taxaStr + '</td>' +
        '<td style="padding:8px;font-size:.7rem">' + payloadHtml + '</td>' +
        '<td style="padding:8px;font-size:.65rem;color:var(--textdim)">' + (l.timestamp ? new Date(l.timestamp).toLocaleString('pt-BR') : '-') + '</td>' +
        '</tr>';
    }).join('');
  } catch (err) {
    console.error('Erro ao carregar laudos:', err);
  }
}

// ── Pagamentos ────────────────────────────────────────────────────────────────
async function updatePagamentos() {
  const companyAddr = document.getElementById('pag-company-filter').value.trim();
  let url = '/api/explorer/payments';
  if (companyAddr) url += '?company=' + encodeURIComponent(companyAddr);
  try {
    const resp = await fetch(url);
    if (!resp.ok) { console.error('Payments fetch error'); return; }
    const payments = await resp.json();
    document.getElementById('pag-count').textContent = (payments ? payments.length : 0) + ' transação(es)';
    const tbody = document.getElementById('pag-body');
    if (!payments || payments.length === 0) {
      tbody.innerHTML = '<tr><td colspan="8" style="text-align:center;color:var(--textdim);padding:30px">Nenhuma transação registrada ainda.</td></tr>';
      return;
    }
    const typeColors = { 'MINT': 'var(--green)', 'TRANSFER': 'var(--amber)', 'BROKER_FEE': '#ff8c00', 'VESSEL_REG': '#cc66ff', 'MISSION_LOG': 'var(--blue)' };
    tbody.innerHTML = payments.slice().reverse().map(p => {
      const color = typeColors[p.type] || 'var(--textdim)';
      const badge = '<span class="badge" style="background:rgba(255,255,255,.07);color:' + color + ';font-size:.6rem">' + p.type + '</span>';
      const isPrivate = p.payload && p.payload === '[PRIVADO]';
      const payloadHtml = isPrivate
        ? '<span style="color:var(--red);font-size:.65rem;cursor:pointer;text-decoration:underline;" onclick="revelarPagamento(\'' + p.tx_id + '\')">🔒 PRIVADO (clique)</span>'
        : '<span style="font-size:.65rem;color:var(--textdim)" title="' + (p.payload||'') + '">' + (p.payload ? p.payload.slice(0,50) + (p.payload.length > 50 ? '...' : '') : '-') + '</span>';
      return '<tr style="border-bottom:1px solid rgba(30,58,79,.4);">' +
        '<td style="padding:8px">' + badge + '</td>' +
        '<td style="padding:8px;font-size:.65rem;font-family:monospace;color:var(--textdim)" title="' + (p.from||'') + '">' + (p.from ? p.from.slice(0,14)+'...' : '-') + '</td>' +
        '<td style="padding:8px;font-size:.65rem;font-family:monospace;color:var(--textdim)" title="' + (p.to||'') + '">' + (p.to ? p.to.slice(0,14)+'...' : '-') + '</td>' +
        '<td style="padding:8px;font-size:.72rem;font-family:\'Orbitron\',sans-serif;font-weight:bold;color:' + color + '">' + (p.amount ? p.amount.toFixed(2) : '0.00') + '</td>' +
        '<td style="padding:8px;font-size:.72rem;color:var(--blue)">' + (p.vessel_id || '-') + '</td>' +
        '<td style="padding:8px">' + payloadHtml + '</td>' +
        '<td style="padding:8px;font-size:.7rem;color:var(--green)">#' + (p.block_index !== undefined ? p.block_index : '-') + '</td>' +
        '<td style="padding:8px;font-size:.65rem;color:var(--textdim)">' + (p.timestamp ? new Date(p.timestamp).toLocaleString('pt-BR') : '-') + '</td>' +
        '</tr>';
    }).join('');
  } catch (err) {
    console.error('Erro ao carregar pagamentos:', err);
  }
}

async function revelarLaudo(occurrenceID) {
  const pwd = prompt("Digite a senha de simulação (1234) ou o endereço 0x da empresa contratante para descriptografar:");
  if (!pwd) return;
  try {
    const resp = await fetch('/api/explorer/laudos?company=' + encodeURIComponent(pwd));
    if (!resp.ok) { alert('Acesso negado!'); return; }
    const laudos = await resp.json();
    const found = laudos.find(x => x.occurrence_id === occurrenceID);
    if (found && !found.payload.startsWith('[CONFIDENCIAL')) {
      alert("🔓 LAUDO DESCRIPTOGRAFADO ON-CHAIN:\n\n" + found.payload);
      document.getElementById('laudos-company-filter').value = pwd;
      updateLaudos();
    } else {
      alert("Acesso negado: Senha ou Endereço inválido!");
    }
  } catch (e) {
    alert("Erro de conexão ao descriptografar.");
  }
}

async function revelarPagamento(txID) {
  const pwd = prompt("Digite a senha de simulação (1234) ou o endereço 0x da empresa envolvida para descriptografar os metadados:");
  if (!pwd) return;
  try {
    const resp = await fetch('/api/explorer/payments?company=' + encodeURIComponent(pwd));
    if (!resp.ok) { alert('Acesso negado!'); return; }
    const payments = await resp.json();
    const found = payments.find(x => x.tx_id === txID);
    if (found && found.payload !== '[PRIVADO]') {
      alert("🔓 PAYLOAD DE TRANSAÇÃO DESCRIPTOGRAFADO:\n\n" + found.payload);
      document.getElementById('pag-company-filter').value = pwd;
      updatePagamentos();
    } else {
      alert("Acesso negado: Senha ou Endereço inválido!");
    }
  } catch (e) {
    alert("Erro de conexão ao descriptografar.");
  }
}

// ── Polling Periódico Dinâmico para a Aba Ativa ──
setInterval(() => {
  if (currentTab === 'explorer') updateExplorer();
  if (currentTab === 'laudos') updateLaudos();
  if (currentTab === 'pagamentos') updatePagamentos();
}, 2000);
</script>
</body>
</html>`

// ── Funções de Apoio do Explorer ──────────────────────────────────────────────

func obterLiderAPIUrl() string {
	liderMu.RLock()
	lid := liderAtual
	liderMu.RUnlock()

	estadoMu.RLock()
	defer estadoMu.RUnlock()

	for _, b := range brokers {
		if b.ID == lid && b.Vivo {
			host, _, err := net.SplitHostPort(b.Addr)
			if err != nil {
				host = "localhost"
			}
			_, portStr, _ := net.SplitHostPort(b.Addr)
			var port int
			fmt.Sscanf(portStr, "%d", &port)
			return fmt.Sprintf("http://%s:%d", host, port+1000)
		}
	}

	for _, b := range brokers {
		if b.Vivo {
			host, _, err := net.SplitHostPort(b.Addr)
			if err != nil {
				host = "localhost"
			}
			_, portStr, _ := net.SplitHostPort(b.Addr)
			var port int
			fmt.Sscanf(portStr, "%d", &port)
			return fmt.Sprintf("http://%s:%d", host, port+1000)
		}
	}
	return ""
}

func handleExplorerBlocks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	apiURL := obterLiderAPIUrl()
	if apiURL == "" {
		http.Error(w, "Sem brokers ativos para consultar a blockchain", http.StatusServiceUnavailable)
		return
	}

	resp, err := http.Get(apiURL + "/blockchain/blocks")
	if err != nil {
		http.Error(w, fmt.Sprintf("Erro ao conectar ao broker líder: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	io.Copy(w, resp.Body)
}

func handleExplorerMempool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	apiURL := obterLiderAPIUrl()
	if apiURL == "" {
		http.Error(w, "Sem brokers ativos para consultar a mempool", http.StatusServiceUnavailable)
		return
	}

	resp, err := http.Get(apiURL + "/blockchain/mempool")
	if err != nil {
		http.Error(w, fmt.Sprintf("Erro ao conectar ao broker líder: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	io.Copy(w, resp.Body)
}

type CompanyState struct {
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Balance float64  `json:"balance"`
	Vessels []string `json:"vessels"`
}

func handleExplorerBalances(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	apiURL := obterLiderAPIUrl()
	if apiURL == "" {
		http.Error(w, "Sem brokers ativos para consultar saldos", http.StatusServiceUnavailable)
		return
	}

	resp, err := http.Get(apiURL + "/blockchain/blocks")
	if err != nil {
		http.Error(w, fmt.Sprintf("Erro ao obter blocos: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	var blocks []models.Block
	if err := json.NewDecoder(resp.Body).Decode(&blocks); err != nil {
		http.Error(w, "Falha ao decodificar blocos", http.StatusInternalServerError)
		return
	}

	validatorsMap := make(map[string]string)
	for _, valID := range []string{"B1", "B2", "B3", "B4"} {
		pub := blockchain.GetValidatorPubKey(valID)
		addr := wallet.GetAddress(pub)
		validatorsMap[addr] = "Broker " + valID
	}

	getCompanyName := func(addr string, payload string) string {
		if payload != "" && payload != "Empresa Desconhecida" {
			return payload
		}
		if name, ok := validatorsMap[addr]; ok {
			return name
		}
		return "Empresa Desconhecida"
	}

	companies := make(map[string]*CompanyState)
	for _, block := range blocks {
		for _, tx := range block.Transactions {
			switch tx.Type {
			case models.TxRegister:
				if _, exists := companies[tx.From]; !exists {
					companies[tx.From] = &CompanyState{
						Name:    tx.Payload,
						Address: tx.From,
						Balance: 0,
						Vessels: []string{},
					}
				} else {
					companies[tx.From].Name = tx.Payload
				}
			case models.TxMint:
				if _, exists := companies[tx.To]; !exists {
					companies[tx.To] = &CompanyState{
						Name:    getCompanyName(tx.To, ""),
						Address: tx.To,
						Balance: 0,
						Vessels: []string{},
					}
				}
				companies[tx.To].Balance += tx.Amount
			case models.TxTransfer:
				if _, exists := companies[tx.From]; !exists {
					companies[tx.From] = &CompanyState{
						Name:    getCompanyName(tx.From, ""),
						Address: tx.From,
						Balance: 0,
						Vessels: []string{},
					}
				}
				if _, exists := companies[tx.To]; !exists {
					companies[tx.To] = &CompanyState{
						Name:    getCompanyName(tx.To, ""),
						Address: tx.To,
						Balance: 0,
						Vessels: []string{},
					}
				}
				companies[tx.From].Balance -= tx.Amount
				companies[tx.To].Balance += tx.Amount
			case models.TxVesselReg:
				if _, exists := companies[tx.From]; !exists {
					companies[tx.From] = &CompanyState{
						Name:    getCompanyName(tx.From, ""),
						Address: tx.From,
						Balance: 0,
						Vessels: []string{},
					}
				}
				found := false
				for _, v := range companies[tx.From].Vessels {
					if v == tx.VesselID {
						found = true
						break
					}
				}
				if !found {
					companies[tx.From].Vessels = append(companies[tx.From].Vessels, tx.VesselID)
				}
			}
		}
	}

	var list []*CompanyState
	for _, c := range companies {
		list = append(list, c)
	}

	json.NewEncoder(w).Encode(list)
}

// ── HTML do Explorer ──────────────────────────────────────────────────────────

// handleExplorerLaudos: proxy para /blockchain/laudos do broker mais disponível
func handleExplorerLaudos(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	apiURL := obterLiderAPIUrl()
	if apiURL == "" {
		w.Write([]byte("[]"))
		return
	}
	targetURL := apiURL + "/blockchain/laudos"
	if company := r.URL.Query().Get("company"); company != "" {
		targetURL += "?company=" + company
	}
	resp, err := http.Get(targetURL)
	if err != nil {
		w.Write([]byte("[]"))
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}

// handleExplorerPayments: proxy para /blockchain/payments do broker mais disponível
func handleExplorerPayments(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	apiURL := obterLiderAPIUrl()
	if apiURL == "" {
		w.Write([]byte("[]"))
		return
	}
	targetURL := apiURL + "/blockchain/payments"
	if company := r.URL.Query().Get("company"); company != "" {
		targetURL += "?company=" + company
	}
	resp, err := http.Get(targetURL)
	if err != nil {
		w.Write([]byte("[]"))
		return
	}
	defer resp.Body.Close()
	io.Copy(w, resp.Body)
}


const explorerHTML = `<!DOCTYPE html>
<html lang="pt-BR">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>HormuzChain — Explorer</title>
<link href="https://fonts.googleapis.com/css2?family=Orbitron:wght@400;700;900&family=Share+Tech+Mono&display=swap" rel="stylesheet">
<style>
:root {
  --bg:       #03060b;
  --bg2:      #080f18;
  --bg3:      #0f1c2d;
  --border:   #1b3247;
  --green:    #00ff66;
  --amber:    #ffbb00;
  --red:      #ff3333;
  --blue:     #00ccff;
  --purple:   #cc66ff;
  --dim:      #2f5575;
  --text:     #d7ecf5;
  --textdim:  #6c9ebb;
}

* { box-sizing: border-box; margin: 0; padding: 0; }

body {
  font-family: 'Share Tech Mono', monospace;
  background: var(--bg);
  color: var(--text);
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  overflow-x: hidden;
}

header {
  background: var(--bg2);
  border-bottom: 1px solid var(--border);
  padding: 15px 25px;
  display: flex;
  align-items: center;
  gap: 20px;
  position: relative;
}
header::after {
  content: '';
  position: absolute;
  bottom: 0; left: 0; right: 0;
  height: 1px;
  background: linear-gradient(90deg, transparent, var(--blue), transparent);
  opacity: 0.5;
}

.logo {
  font-family: 'Orbitron', sans-serif;
  font-size: 1.2rem;
  font-weight: 900;
  letter-spacing: .15em;
  color: var(--blue);
  text-shadow: 0 0 20px rgba(0,204,255,.4);
}
.logo span { color: var(--textdim); font-weight: 400; }

.header-stats {
  display: flex;
  gap: 30px;
  margin-left: auto;
}
.hstat {
  display: flex;
  flex-direction: column;
  align-items: center;
  font-size: .65rem;
  color: var(--textdim);
}
.hstat b {
  font-size: 1.4rem;
  color: var(--text);
  font-family: 'Orbitron', sans-serif;
}
.hstat b.blue { color: var(--blue); }
.hstat b.green { color: var(--green); }
.hstat b.amber { color: var(--amber); }

.main {
  display: grid;
  grid-template-columns: 450px 1fr;
  grid-template-rows: 1fr;
  gap: 15px;
  flex: 1;
  padding: 15px;
  overflow: hidden;
}
@media (max-width: 1000px) {
  .main { grid-template-columns: 1fr; }
}

.panel {
  background: var(--bg2);
  border: 1px solid var(--border);
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  overflow: hidden;
  box-shadow: 0 4px 20px rgba(0,0,0,0.5);
}

.panel-title {
  font-family: 'Orbitron', sans-serif;
  font-size: .85rem;
  font-weight: 700;
  letter-spacing: .15em;
  color: var(--blue);
  padding: 12px 18px;
  border-bottom: 1px solid var(--border);
  background: rgba(0,204,255,0.03);
}

.panel-body {
  flex: 1;
  overflow-y: auto;
  padding: 15px;
}

.table-balances {
  width: 100%;
  border-collapse: collapse;
}
.table-balances th {
  text-align: left;
  padding: 8px 10px;
  font-size: .7rem;
  color: var(--blue);
  border-bottom: 2px solid var(--border);
}
.table-balances td {
  padding: 10px;
  font-size: .75rem;
  border-bottom: 1px solid rgba(27,50,71,0.5);
}
.balance-val {
  font-family: 'Orbitron', sans-serif;
  font-weight: bold;
  color: var(--green);
}

.block-card {
  background: var(--bg3);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 12px;
  margin-bottom: 10px;
  transition: all 0.2s;
}
.block-card:hover {
  border-color: var(--blue);
  box-shadow: 0 0 10px rgba(0,204,255,0.2);
}
.block-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.block-index {
  font-family: 'Orbitron', sans-serif;
  font-weight: bold;
  color: var(--blue);
}
.block-time {
  font-size: .7rem;
  color: var(--textdim);
}
.block-hash {
  font-size: .65rem;
  color: var(--textdim);
  background: rgba(0,0,0,0.3);
  padding: 4px 6px;
  border-radius: 3px;
  word-break: break-all;
  margin-bottom: 8px;
}
.block-txs-count {
  font-size: .7rem;
  color: var(--green);
}
.block-signatures {
  font-size: .62rem;
  color: var(--amber);
  margin-top: 5px;
}

.tx-item {
  border-left: 2px solid var(--dim);
  padding-left: 10px;
  margin: 8px 0;
  font-size: .7rem;
}
.tx-item.MINT { border-left-color: var(--green); }
.tx-item.REGISTER { border-left-color: var(--blue); }
.tx-item.TRANSFER { border-left-color: var(--amber); }
.tx-item.VESSEL { border-left-color: var(--purple); }

.mempool-card {
  background: var(--bg3);
  border: 1px solid var(--border);
  border-radius: 4px;
  padding: 10px;
  margin-bottom: 8px;
  font-size: .7rem;
}

.badge {
  font-size: .6rem;
  padding: 2px 6px;
  border-radius: 3px;
  font-weight: bold;
  text-transform: uppercase;
}
.badge.mint { background: rgba(0,255,102,0.15); color: var(--green); }
.badge.reg { background: rgba(0,204,255,0.15); color: var(--blue); }
.badge.transfer { background: rgba(255,187,0,0.15); color: var(--amber); }
.badge.vessel { background: rgba(204,102,255,0.15); color: var(--purple); }

body::before {
  content: '';
  position: fixed;
  inset: 0;
  background: repeating-linear-gradient(
    0deg,
    transparent,
    transparent 2px,
    rgba(0,0,0,.08) 2px,
    rgba(0,0,0,.08) 4px
  );
  pointer-events: none;
  z-index: 9999;
}
</style>
</head>
<body>

<header>
  <div class="logo">HORMUZCHAIN <span>// BLOCKCHAIN EXPLORER</span></div>
  <div class="header-stats">
    <div class="hstat"><b class="blue" id="stat-blocks">0</b>BLOCOS</div>
    <div class="hstat"><b class="green" id="stat-txs">0</b>TRANSAÇÕES</div>
    <div class="hstat"><b class="amber" id="stat-mempool">0</b>MEMPOOL</div>
  </div>
</header>

<div class="main">
  <!-- Coluna Esquerda: Saldo de Empresas -->
  <div class="panel">
    <div class="panel-title">SALDOS E CARTEIRAS (ELIS)</div>
    <div class="panel-body">
      <table class="table-balances">
        <thead>
          <tr>
            <th>EMPRESA</th>
            <th>ENDEREÇO</th>
            <th>SALDO ELIS</th>
            <th>NAVIOS (VESSELS)</th>
          </tr>
        </thead>
        <tbody id="balances-body">
          <tr><td colspan="4" style="text-align:center;color:var(--textdim)">Carregando dados...</td></tr>
        </tbody>
      </table>
    </div>
    
    <div class="panel-title" style="border-top: 1px solid var(--border)">TRANSAÇÕES EM MEMPOOL</div>
    <div class="panel-body" id="mempool-body">
      <div style="text-align:center;color:var(--textdim);font-size:.75rem">Nenhuma transação pendente.</div>
    </div>
  </div>

  <!-- Coluna Direita: Blocos da Cadeia -->
  <div class="panel">
    <div class="panel-title">HISTÓRICO DE BLOCOS (PoA CONSENSUS)</div>
    <div class="panel-body" id="blocks-body">
      <div style="text-align:center;color:var(--textdim)">Carregando blocos...</div>
    </div>
  </div>
</div>

<script>
function formatTime(isoStr) {
  if (!isoStr || isoStr.startsWith('0001-01-01')) return '--';
  try {
    const d = new Date(isoStr);
    return d.toLocaleString('pt-BR');
  } catch (e) { return '--'; }
}

async function updateData() {
  try {
    // 1. Fetch Blocks
    const rBlocks = await fetch('/api/explorer/blocks');
    if (!rBlocks.ok) return;
    const blocks = await rBlocks.json();
    
    // 2. Fetch Mempool
    const rMempool = await fetch('/api/explorer/mempool');
    if (!rMempool.ok) return;
    const mempool = await rMempool.json();

    // 3. Fetch Balances
    const rBalances = await fetch('/api/explorer/balances');
    if (!rBalances.ok) return;
    const balances = await rBalances.json();

    // Update Stats
    document.getElementById('stat-blocks').textContent = blocks.length;
    document.getElementById('stat-mempool').textContent = mempool.length;
    
    let totalTxs = 0;
    blocks.forEach(b => totalTxs += (b.transactions ? b.transactions.length : 0));
    document.getElementById('stat-txs').textContent = totalTxs;

    // Render Balances
    const balancesBody = document.getElementById('balances-body');
    if (balances.length === 0) {
      balancesBody.innerHTML = '<tr><td colspan="4" style="text-align:center;color:var(--textdim)">Nenhuma empresa registrada.</td></tr>';
    } else {
      balances.sort((a,b) => b.balance - a.balance);
      balancesBody.innerHTML = balances.map(c => {
        const vesselsStr = c.vessels && c.vessels.length > 0 ? c.vessels.join(', ') : 'Nenhum';
        return '<tr>' +
          '<td style="font-weight:bold;color:var(--blue)">' + c.name + '</td>' +
          '<td style="font-size:.65rem;color:var(--textdim);font-family:monospace">' + c.address.slice(0, 16) + '...</td>' +
          '<td class="balance-val">' + c.balance.toFixed(2) + ' ELIS</td>' +
          '<td style="font-size:.68rem;color:var(--textdim)">' + vesselsStr + '</td>' +
          '</tr>';
      }).join('');
    }

    // Render Mempool
    const mempoolBody = document.getElementById('mempool-body');
    if (mempool.length === 0) {
      mempoolBody.innerHTML = '<div style="text-align:center;color:var(--textdim);font-size:.75rem">Nenhuma transação pendente.</div>';
    } else {
      mempoolBody.innerHTML = mempool.map(tx => {
        let badge = '';
        let details = '';
        if (tx.type === 'MINT') {
          badge = '<span class="badge mint">MINT</span>';
          details = 'Mint de ' + tx.amount + ' ELIS para ' + tx.to.slice(0, 8);
        } else if (tx.type === 'REGISTER') {
          badge = '<span class="badge reg">REGISTER</span>';
          details = 'Registro de empresa: ' + tx.payload;
        } else if (tx.type === 'TRANSFER') {
          badge = '<span class="badge transfer">TRANSFER</span>';
          details = tx.from.slice(0,8) + ' envia ' + tx.amount + ' ELIS para ' + tx.to.slice(0,8) + (tx.payload ? ' ('+tx.payload+')' : '');
        } else if (tx.type === 'VESSEL_REG') {
          badge = '<span class="badge vessel">VESSEL</span>';
          details = 'Registro de Navio: ' + tx.vessel_id + ' por ' + tx.from.slice(0,8);
        }
        return '<div class="mempool-card">' +
          '<div style="display:flex;justify-content:space-between;margin-bottom:5px"><b>ID: ' + tx.id.slice(0,12) + '...</b>' + badge + '</div>' +
          '<div style="color:var(--textdim);font-size:.65rem">' + details + '</div>' +
          '</div>';
      }).join('');
    }

    // Render Blocks
    const blocksBody = document.getElementById('blocks-body');
    blocksBody.innerHTML = blocks.slice().reverse().map(b => {
      let txsHTML = '';
      if (b.transactions && b.transactions.length > 0) {
        txsHTML = b.transactions.map(tx => {
          let typeStr = 'TRANSFER';
          let badgeClass = 'TRANSFER';
          let desc = '';
          if (tx.type === 'MINT') { typeStr = 'MINT'; badgeClass = 'MINT'; desc = tx.amount + ' ELIS -> ' + tx.to.slice(0, 8); }
          else if (tx.type === 'REGISTER') { typeStr = 'REGISTER'; badgeClass = 'REGISTER'; desc = 'Empresa ' + tx.payload; }
          else if (tx.type === 'TRANSFER') { typeStr = 'TRANSFER'; badgeClass = 'TRANSFER'; desc = tx.from.slice(0, 8) + ' enviou ' + tx.amount + ' ELIS para ' + tx.to.slice(0, 8) + (tx.payload ? ' ('+tx.payload+')' : ''); }
          else if (tx.type === 'VESSEL_REG') { typeStr = 'VESSEL'; badgeClass = 'VESSEL'; desc = 'Navio ' + tx.vessel_id; }
          
          return '<div class="tx-item ' + badgeClass + '">' +
            '<b>[' + typeStr + ']</b> ' + desc + '<br/>' +
            '<span style="font-size:.6rem;color:var(--textdim)">TXID: ' + tx.id + '</span>' +
            '</div>';
        }).join('');
      } else {
        txsHTML = '<div style="color:var(--textdim);font-size:.65rem">Nenhuma transação no bloco.</div>';
      }

      const votesCount = b.signature ? b.signature.split(';').filter(x => x).length : 0;
      const consensusLabel = b.index === 0 ? 'GENESIS BLOCK' : votesCount + ' Assinaturas Validadoras (PoA)';

      return '<div class="block-card">' +
        '<div class="block-header">' +
          '<span class="block-index">BLOCO #' + b.index + '</span>' +
          '<span class="block-time">' + formatTime(b.timestamp) + '</span>' +
        '</div>' +
        '<div class="block-hash">HASH: ' + b.hash + '</div>' +
        '<div class="block-hash" style="margin-top:-4px">PREV: ' + b.prev_hash + '</div>' +
        '<div style="margin:10px 0 5px 0;font-weight:bold;font-size:.72rem;color:var(--blue)">TRANSAÇÕES:</div>' +
        '<div style="background:rgba(0,0,0,0.2);padding:8px;border-radius:4px;margin-bottom:8px">' + txsHTML + '</div>' +
        '<div class="block-signatures">✓ ' + consensusLabel + '</div>' +
        '</div>';
    }).join('');

  } catch (err) {
    console.error("Erro ao atualizar dados do explorer: ", err);
  }
}

setInterval(updateData, 2000);
updateData();
</script>
</body>
</html>`


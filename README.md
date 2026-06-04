# HormuzChain — Monitoramento Marítimo e Blockchain (TEC502)

O **HormuzChain** é uma evolução do sistema **HormuzNet** (Problema 2), transformando uma malha cooperativa descentralizada de monitoramento do Estreito de Ormuz em um sistema unificado que inclui **Economia Criptográfica**, **Auditoria de Missões** e **Pagamentos de Escoltas** rodando em uma blockchain própria (Problema 3).

Todo o sistema foi desenvolvido em **Go 1.23** sem o uso de middlewares ou frameworks comerciais de mensageria (como RabbitMQ ou Kafka), utilizando apenas comunicação nativa via **UDP Multicast**, **TCP**, **WebSockets** e um **Consenso Proof of Authority (PoA)** implementado do zero.

---

## 1. Cenário e Objetivo do Projeto

O Estreito de Ormuz é uma das vias marítimas mais estratégicas e críticas do mundo. O sistema foi projetado para não possuir um ponto único de falha:
- **Broker Distribuídos:** Uma malha TCP P2P dinâmica entre Brokers de Setor que se auto-organiza via Descoberta Dinâmica, Protocolo Gossip, Eleição Bully e Ring Failover.
- **Sensores e Drones:** Sensores disparam alertas de ameaças via Multicast UDP. Drones autônomos recebem as tarefas dos Brokers para patrulhar as áreas, com tolerância a quedas e reconexão.
- **Camada Blockchain (HormuzChain):** Introduz um ledger imutável e distribuído mantido pelos Brokers (validadores). As empresas donas dos navios (Maersk, MSC, CMA_CGM, Hapag_Lloyd e ONE) gerenciam fundos em ELIS (moeda nativa) para registrar seus navios e pagar taxas automatizadas pelas escoltas de drones.
- **Monitores Unificados:** Consolidação em tempo real do estado tático dos drones e navios (Dashboard) e do estado do ledger da criptomoeda (Blockchain Explorer) utilizando WebSockets nativos (RFC 6455).

---

## 2. Arquitetura do Sistema e Topologia

O Estreito de Ormuz é dividido logicamente em setores mapeados para os Brokers Validadores. A configuração em escala é a seguinte:

| Região Físico-Lógica | Componentes em Operação |
| :--- | :--- |
| **Noroeste (NW)** | Broker B1 (:6000), Sensores NW, Drones NW |
| **Nordeste (NE)** | Broker B2 (:6001), Sensores NE, Drones NE |
| **Sudoeste (SW)** | Broker B3 (:6002), Sensores SW, Drones SW |
| **Sudeste (SE)** | Broker B4 (:6003), Sensores SE, Drones SE |

Cada Broker acumula a função de nó P2P da malha tática, servidor de Consenso PoA da blockchain e servidor da REST API de carteiras na porta `700X` (onde X = ID do broker).

---

## 3. Bibliotecas Go Utilizadas

A implementação das lógicas críticas e persistentes foi desenvolvida "do zero", utilizando exclusivamente a **Standard Library do Go**, garantindo portabilidade, eficiência e baixo footprint.

### Bibliotecas Core Utilizadas:
- `net` e `net/http`: Gestão das conexões TCP (malha P2P), portas UDP (sensores) e disponibilização do servidor Web/API e WebSockets.
- `crypto/ecdsa`, `crypto/elliptic`, `crypto/sha256`: Implementação do módulo criptográfico do HormuzChain e das carteiras nativas das empresas (`secp256k1`).
- `encoding/json` e `encoding/hex`: Serialização padronizada de pacotes nas conexões P2P, assinaturas de blocos e respostas da API.
- `sync`: Gerenciamento pesado de Mutex (`sync.Mutex` / `sync.RWMutex`) para compartilhar as estruturas de estado entre as threads concorrentes (recebimento de rede, lógica de domínio, consenso blockchain).
- `time` e `math`: Controle preciso da simulação de movimentação física dos navios, controle de heartbeat (Gossip), cálculo das taxas baseadas em distância, ordenação local dos Relógios de Lamport e reposição programada de tokens (faucet).

---

## 4. Estrutura do Código e Módulos

O monorepo está estruturado nos seguintes serviços principais:

- **`code/cmd/broker`**: Núcleo validador. Agrega as funcionalidades P2P, Consenso PoA (assinaturas e aprovações de blocos), e expõe a REST API para o pagamento de escoltas e registro de navios.
- **`code/cmd/vessel`**: Software-agente executado no container de cada navio. Identifica dinamicamente sua empresa via variável de ambiente, assina digitalmente a transação de registro on-chain (`TxVesselReg`), reporta keepalives de localização e gera transações locais de pagamento via assinatura ECDSA em caso de perigo e necessidade de escolta (`TxTransfer`).
- **`code/cmd/drone`**: Veículo Aéreo de escolta. Despachado pelos brokers para intervir na localização dos navios após confirmação dos fundos. Gera laudos pós-missão e transações `TxMissionLog`.
- **`code/cmd/sensor`**: Emula radares e sonares que detectam presenças não amigáveis próximas aos navios, gerando gatilhos via UDP Multicast.
- **`code/cmd/monitor`**: Servidor Web e Central de Operações. Expõe o Dashboard tático em `http://localhost:8085` e atende consultas API para alimentar a aba do "Blockchain Explorer".

### Persistência de Dados
A Blockchain persiste o estado das contas em memória para altíssima performance, garantindo integridade e distribuição do histórico imutável via consenso e blocos.

---

## 5. Dinâmica Cripto-Econômica do HormuzChain

1. **Moeda Oficial (ELIS):** Moeda utilitária para garantir a operação militar com recursos das empresas de frete naval.
2. **Registro de Empresas e Navios:** As empresas de logística (ex: *Maersk, MSC, CMA_CGM, Hapag_Lloyd e ONE*) começam financiadas no bloco gênese. Para que seus navios transitem, elas precisam registrar a identidade (`TxVesselReg`) na blockchain.
3. **Escolta de Emergência:** Quando os sensores identificam que um navio está em área de alto risco e o navio "concorda" necessitar de escolta:
   - A empresa (no terminal do próprio `vessel`) emite uma transferência criptograficamente assinada (`TxTransfer`) com o valor estimado em ELIS.
   - O Broker recebe, valida os fundos e insere a transação no Mempool.
   - O bloco é minerado no próximo intervalo e o drone é fisicamente despachado.
   - Caso não haja fundos suficientes para pagar a escolta, a transação é rejeitada na verificação de blocos e o drone NÃO é despachado, sendo emitida uma ocorrência "Ignorada por Falta de Fundo".

---

## 6. Como Executar e Interagir

Toda a arquitetura é orquestrada através do **Docker Compose**, simulando nativamente um ecossistema distribuído de contêineres autônomos.

### Subindo toda a escala da Simulação
Para provisionar toda a frota simultânea e os validadores interconectados:
```bash
./subir_escala.sh
```
O script fará o build paralelo de todos os executáveis Go, subirá:
- 4 Brokers (rede PoA)
- 3 Drones para escolta e patrulha 
- 5 Sensores diversos de identificação
- O painel Monitor Central
- **10 Navios** (2 representantes autônomos para cada uma das 5 gigantes do frete logístico, gerando navegação dinâmica e pagamentos criptográficos).

### Observando o Painel (Monitor)
Acesse a URL em seu navegador para ver a malha P2P rodando em tempo real (painel de controle, mapa tático e log P2P) bem como as visões do Ledger da Blockchain:
```
http://localhost:8085
```

### Inspecionando as transações locais completas da Empresa (Vessel Logs)
Para ilustrar a descentralização de chaves e carteiras, cada navio formata e documenta a geração e o envio de suas assinaturas de pagamento para o broker, diretamente no terminal do próprio cliente, sem depender do monitor.

Para assistir a geração dos payloads JSON assombrosamente criptográficos na prática (ex: assinaturas ECDSA em Base64 `secp256k1` e SHA-256 encadeado):
```bash
# Acompanhe o log da empresa MAERSK (navio 1)
docker logs -f hormuzchain_vessel_maersk_01

# Ou acompanhe o log da empresa ONE
docker logs -f hormuzchain_vessel_one_01
```

### Derrubando a Simulação
Para encerrar a simulação e fazer o cleanup de todos os recursos Docker atrelados de forma limpa:
```bash
docker-compose -f docker-compose-escala.yml down --remove-orphans
```

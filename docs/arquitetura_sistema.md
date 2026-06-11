# Arquitetura do Sistema HormuzChain

Este documento descreve detalhadamente a arquitetura do sistema **HormuzChain**, o fluxo do consenso **Proof of Authority (PoA)**, o funcionamento de cada módulo e onde cada funcionalidade está implementada no código, incluindo referências diretas de arquivos e funções.

---

## 1. Quem gera o bloco na Blockchain?

A geração de blocos no HormuzChain é governada por um consenso **Proof of Authority (PoA) do tipo Round-Robin** implementado do zero. 

### O Fluxo de Proposta e Consenso:
1. **Definição de Validadores:** Existem 4 validadores autorizados definidos na rede (`B1`, `B2`, `B3` e `B4`), cujas chaves públicas são geradas deterministicamente a partir de seus IDs.
   * *Referência:* `Validators` em [consensus.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/consensus.go#L7).
2. **Seleção do Proponente:** O proponente autorizado a gerar o bloco $N$ é definido de forma rotativa pelo resto da divisão do índice do bloco pelo número de validadores:
   $$\text{Proponente} = \text{Validators}[N \pmod 4]$$
   * *Referência:* Função `GetProposer` em [consensus.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/consensus.go#L10).
3. **Loop de Consenso (Proposta de Bloco):** Cada Broker executa uma thread periódica a cada 10 segundos. Se o Broker atual for o proponente da vez para o próximo bloco e tiver transações pendentes no seu mempool local:
   * Ele empacota até 5 transações.
   * Assina o bloco digitalmente usando sua chave privada ECDSA.
   * Insere sua própria assinatura na lista de votos.
   * Transmite a proposta (`MsgBlockProposal`) via TCP para todos os brokers vizinhos.
   * *Referência:* Função `loopConsensus` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L1027).
4. **Votação (Validação da Proposta):** Ao receber uma proposta de bloco, os outros brokers verificam se:
   * O proponente é de fato o esperado para aquele índice.
   * A assinatura do bloco confere com a chave pública do proponente.
   * O índice do bloco e o hash do bloco anterior estão sequencialmente corretos.
   * Se for válido, o broker assina o bloco com sua própria chave privada e devolve uma mensagem de voto (`MsgBlockVote`).
   * *Referência:* Função `handleBlockProposal` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L1076).
5. **Commit (Confirmação do Bloco):** Quando o proponente acumula **pelo menos 3 votos válidos (quórum de 3/4)**:
   * Ele junta todas as assinaturas separadas por ponto e vírgula (`;`) no campo `Signature` do bloco.
   * Adiciona o bloco à sua própria Blockchain local e limpa as transações confirmadas do mempool.
   * Distribui a mensagem de Commit (`MsgBlockCommit`) para a rede.
   * Aplica os efeitos das transações confirmadas (ex: debita taxas, altera status de ocorrências de navios para `PAGO` e as enfileira para atendimento imediato do drone).
   * *Referência:* Função `handleBlockVote` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L1119).
6. **Recebimento de Commit:** Os outros brokers recebem o commit, validam se ele contém as $\ge 3$ assinaturas válidas usando chaves públicas dos validadores conhecidos e, em caso positivo, adicionam o bloco à sua cadeia local e aplicam as transações.
   * *Referência:* Funções `handleBlockCommit` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L1168) e `VerifyBlockVotes` em [consensus.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/consensus.go#L35).

---

## 2. Mapa Geral de Módulos e Funcionalidades

O projeto está dividido em duas partes principais: **Serviços executáveis (`code/cmd/`)** e **Bibliotecas compartilhadas (`code/internal/`)**.

```
HormuzChain/
├── code/
│   ├── cmd/
│   │   ├── broker/      # Servidor central de setor (P2P + Blockchain PoA + API REST)
│   │   ├── vessel/      # Cliente simulador de navios das empresas
│   │   ├── drone/       # Cliente simulador de drones de patrulha
│   │   ├── sensor/      # Cliente simulador de sensores físicos (sonares/radares)
│   │   ├── monitor/     # Servidor web do dashboard e Explorer da Blockchain
│   │   └── cli/         # CLI para comandos manuais de carteira/registro
│   └── internal/
│       ├── blockchain/  # Regras do ledger, PoA, validação de transações e persistência
│       ├── wallet/      # Módulo criptográfico (chaves ECDSA secp256k1, assinatura e endereço)
│       ├── api/         # Rotas e endpoints HTTP expostos pelos brokers
│       ├── pricing/     # Cálculo dinâmico de tarifas de escolta
│       ├── models/      # Estrutura de dados compartilhados (Block, Transaction, etc.)
│       └── fila/        # Fila de prioridades local para despacho
```

---

## 3. Guia de Implementação: Onde Encontro Cada Função?

A tabela abaixo descreve onde as principais lógicas de negócio do sistema estão implementadas:

### 3.1. Criptografia e Carteiras (Módulo `wallet` & `blockchain`)
* **Geração de Chaves Determinísticas:** Permite recuperar chaves privadas e endereços a partir de uma "semente" string (o nome da empresa).
  * *Onde está:* Função `DeterministicKey` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L52).
* **Assinatura Digital (ECDSA):** Assina transações com a chave privada.
  * *Onde está:* Função `Sign` em [wallet.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/wallet/wallet.go#L36).
* **Derivação de Endereço:** Gera a carteira `0x...` fazendo o hash SHA-256 da chave pública e truncando em 20 caracteres.
  * *Onde está:* Função `GetAddress` em [wallet.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/wallet/wallet.go#L29).

### 3.2. Ledger da Blockchain (Módulo `blockchain`)
* **Estrutura do Bloco e Transação:** Define os dados armazenados na rede.
  * *Onde está:* Structs `Transaction` e `Block` em [transaction.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/models/transaction.go).
* **Validação de Transações (Mempool):** Verifica regras como saldo suficiente, duplicidade, registro prévio de empresa para cadastrar navio, etc.
  * *Onde está:* Função `AddTxToMempool` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L259).
* **Persistência em Arquivo:** Grava a cadeia de blocos local no arquivo `chain_<broker_id>.json` para não perder dados ao reiniciar.
  * *Onde está:* Funções `Save` e `Load` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L118-L172).

### 3.3. Malha P2P, Gossip e Eleição (Módulo `broker`)
* **Descoberta Dinâmica:** Mecanismo onde brokers informam seu endereço ao líder inicial (`B1`), e este repassa a lista a todos para autoconexão.
  * *Onde está:* Funções `conectarLider` e `handleBlockProposal` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L1209) sob mensagens do tipo `MsgDiscovery` e `MsgPeerList`.
* **Detecção de Falhas (Heartbeat):** Loop periódico de mensagens de keepalive para verificar se vizinhos estão ativos.
  * *Onde está:* Funções `loopHeartbeat` e `loopDetectarFalhas` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L1400-L1460).
* **Failover do Anel (Ring Failover):** Lógica que determina qual broker vizinho assume as ocorrências de um setor cujo broker caiu.
  * *Onde está:* Função `responsavelPorSetor` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L325).

### 3.4. Sensores e Despacho de Drones (Módulos `sensor`, `broker` & `drone`)
* **Detecção de Navios:** Sensores realizam polling na API do broker local, descobrem coordenadas dos navios e geram alertas críticos (`CriticidadeAlta`) se entrarem no raio de ação.
  * *Onde está:* Função `gerarLeitura` em [sensor_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/sensor/sensor_main.go#L115).
* **Despacho e Exclusão Mútua:** Brokers selecionam o drone disponível mais próximo e enviam ordem de voo. A concorrência é tratada para evitar que dois brokers despachem drones para a mesma ocorrência.
  * *Onde está:* Funções `tentarDespachar` e `droneMaisProximo` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L798-L754).
* **Laudo de Conclusão de Missão:** Drone grava dados do serviço físico finalizado e gera uma transação on-chain do tipo `TxMissionLog`.
  * *Onde está:* Função `processarMensagemDrone` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L690).

### 3.5. Simulação do Navio (Módulo `vessel`)
* **Movimentação do Navio:** Movimentação contínua simulada (com bounce nas margens físicas) e keepalive de posição enviado ao broker.
  * *Onde está:* `main` em [vessel_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/vessel/vessel_main.go#L25-L94).
* **Auto-Pagamento:** Monitora a API REST para ver se há ocorrências de socorro pendentes para si em `AGUARDANDO_PAGAMENTO`. Caso encontre, assina a ocorrência digitalmente usando a chave privada e submete a transação de pagamento.
  * *Onde está:* Função `autoPayLoop` em [vessel_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/vessel/vessel_main.go#L96).

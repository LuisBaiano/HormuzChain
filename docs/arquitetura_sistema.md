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

---

## 4. Guia de Defesa: Alinhamento com o Barema do Professor

Este guia mapeia diretamente os critérios de avaliação descritos no Barema do Professor com a implementação técnica do HormuzChain, servindo de roteiro para a arguição de 30 minutos.

### 4.1. Arquitetura Genuinamente Descentralizada
* **Sem Banco de Dados Centralizado:** Não existe um banco de dados central (como PostgreSQL ou MongoDB) e nem um "servidor de saldos". Cada nó Broker em execução mantém sua própria cópia independente do ledger em um arquivo JSON local chamado `chain_<broker_id>.json`.
  * *Verificação técnica:* Arquivos criados na pasta de execução dos brokers (`chain_B1.json`, `chain_B2.json`, etc.).
  * *Resiliência a Falhas (derrubar nós):* Se o broker líder cair, o sistema continua operando normalmente. O anel distribui a responsabilidade do setor que caiu para o vizinho imediato usando a função `responsavelPorSetor` (failover do anel) em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L325).
  * *Escolha da Tecnologia (Arguição):* Desenvolveu-se uma blockchain própria em Go do zero para fins pedagógicos de modo a demonstrar o domínio pleno do funcionamento de consenso P2P, hashing, árvores/estruturas de blocos, assinaturas de validadores e controle fino da concorrência sem depender de abstrações prontas de terceiros.

### 4.2. Comunicação e Consenso P2P
* **Propagação de Transações e Blocos:** Os nós conectam-se via sockets TCP persistentes usando uma rede P2P malha (mesh) dinâmica (descoberta iniciada via `MsgDiscovery`). Novas transações e blocos propostos são propagados via gossip simples por `MsgTxBroadcast` e `MsgBlockProposal`.
  * *Referência:* Função `handleMensagem` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L540).
* **Mecanismo de Consenso Finalista (PoA Round-Robin):**
  * O proponente do bloco $N$ é definido de forma previsível e determinística por $N \pmod 4$.
  * O bloco só entra no ledger após obter $\ge 3$ assinaturas válidas de validadores conhecidos (quórum $\ge 3/4$), eliminando forks e garantindo finalidade imediata.
  * *Resolução de Conflitos (Forks):* Como apenas o validador da vez pode propor o bloco naquela altura e o quórum de validadores assina apenas uma proposta válida por vez, forks são impossíveis no estado normal de rede.

### 4.3. Gestão de Ativos e Saldos Dinâmicos (On-chain Balance)
* **Saldos Derivados do Ledger:** Os saldos das empresas **não** são variáveis estáticas. Eles são recalculados dinamicamente percorrendo o histórico completo de transações gravadas nos blocos da blockchain.
  * *Referência:* Função `CalculateBalances` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L320).
* **Transações Autenticadas por Assinatura Digital:** Cada transação de transferência ou pagamento contém uma assinatura criptográfica ECDSA gerada a partir da chave privada (secp256k1) da empresa de origem. Sem a assinatura correspondente à chave pública/endereço de origem, a transação é rejeitada na mempool.
  * *Referência:* Função `VerifyTxSignature` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L297).

### 4.4. Prevenção de Duplo Gasto
* **Fluxo de Validação:** A validação ocorre em dois momentos críticos:
  1. **Antes de Inserir na Mempool:** O broker calcula o saldo atual da empresa e desconta as transações pendentes dela na mempool. Se a nova transação exceder o saldo líquido, ela é rejeitada imediatamente (`AddTxToMempool` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L259)).
  2. **Ao Validar o Bloco Proposto:** Durante o consenso, todos os brokers validadores re-calculam o saldo final da empresa aplicando as transações em ordem cronológica. Se houver duplo gasto concorrente que passou da mempool local, a transação excedente falha na validação do bloco e o bloco é invalidado.

### 4.5. Requisição, Pagamento e Alocação de Escoltas
* **Condicionado ao Pagamento:** Um drone de escolta só é despachado após a transação de pagamento correspondente ser confirmada e registrada em um bloco da blockchain.
* **Prevenção de Alocação Duplicada:** A alocação exclusiva é tratada na máquina de estados de ocorrências do broker, usando exclusão mútua distribuída para garantir que um drone atenda apenas um chamado de cada vez.
  * *Referência:* Função `tentarDespachar` em [broker_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/broker/broker_main.go#L798).

### 4.6. Log de Operações Imutável e Auditoria de Laudos
* **Registro de Laudos On-chain:** Ao concluir uma escolta, o drone emite um relatório que é empacotado pelo broker em uma transação do tipo `MISSION_LOG` (`TxMissionLog`) com metadados detalhando o drone, navio, status final, e coordenadas. A imutabilidade é garantida porque modificar um registro alterará o hash do bloco, invalidando toda a cadeia de hashes subsequente e as assinaturas dos validadores (PoA).
* **Transparência vs. Privacidade no Consórcio:**
  * Em consórcios reais, dados de laudos e pagamentos são privados das empresas envolvidas. Assim, os metadados brutos e laudos são blindados on-chain.
  * O dashboard exibe a existência pública do laudo, mas com a descrição mascarada como `🔒 CONFIDENCIAL` (para laudos) e `🔒 PRIVADO` (para pagamentos).
* **Bypass de Auditoria / Simulação ("1234"):**
  * Para fins de **Auditabilidade sem permissões especiais** pelo professor em laboratório (tempo de apresentação curto), implementou-se um mecanismo de auditoria com senha.
  * Ao clicar sobre `🔒 CONFIDENCIAL` ou `🔒 PRIVADO` no monitor, um prompt é exibido. Inserir a senha `"1234"` (ou o endereço hexadecimal da empresa contratante) descriptografa os dados em tempo real chamando a API do broker correspondente, provando que a informação reside no ledger de forma segura, auditável e recuperável sem fricção.
  * *Referências de Código:*
    * Bypass em Go: `GetLaudos` e `GetPaymentHistory` em [chain.go](file:///home/luis/Área de Trabalho/HormuzChain/code/internal/blockchain/chain.go#L503) (valida `companyAddr == "1234"`).
    * Bypass em JS: `revelarLaudo` e `revelarPagamento` em [monitor_main.go](file:///home/luis/Área de Trabalho/HormuzChain/code/cmd/monitor/monitor_main.go).


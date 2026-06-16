#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
# HormuzNet — menu.sh
# Menu interativo para gerenciamento da simulação HormuzChain e transações financeiras.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# ── Detecta docker compose (v2 plugin) ou docker-compose (v1 standalone) ────
if docker compose version &>/dev/null 2>&1; then
    DOCKER_COMPOSE="docker compose"
elif command -v docker-compose &>/dev/null; then
    DOCKER_COMPOSE="docker-compose"
else
    echo -e "\e[1;31m[ERRO] Nenhuma versão do docker compose encontrada!\e[0m"
    echo "       Instale o Docker Compose antes de continuar."
    exit 1
fi
echo -e "\e[0;90m[INFO] Usando: $DOCKER_COMPOSE\e[0m"

# Garante que o hormuz_cli esteja compilado
if [ ! -f "./hormuz_cli" ]; then
    echo "Compilando CLI financeiro (hormuz_cli)..."
    (cd code && go build -o ../hormuz_cli ./cmd/cli/cli_main.go)
fi

# Função para exibir o cabeçalho
header() {
    clear
    echo -e "\e[1;36m╔══════════════════════════════════════════════════════════╗"
    echo -e "║              HormuzChain — Painel de Controle            ║"
    echo -e "╚══════════════════════════════════════════════════════════╝\e[0m"
    echo ""
}

# Pegar o IP local (padrão)
LOCAL_IP=$(hostname -I | awk '{print $1}')

while true; do
    header
    echo -e "\e[1;33mEscolha um componente para subir neste PC:\e[0m"
    echo "1) Subir Broker Líder (Setor Noroeste/B1)"
    echo "2) Subir Brokers Adicionais (Seguidores B2-B4)"
    echo "3) Subir Monitor (Dashboard na 8085 & Explorer na 8086)"
    echo "4) Subir Drones (2 por setor/broker)"
    echo "5) Subir Sensores (2 por setor/broker)"
    echo "6) Subir Simulação Completa (docker-compose-all.yml)"
    echo "7) Parar e Limpar Todos os Containers"
    echo ""
    echo -e "\e[1;35mTransações Financeiras & Blockchain (ELIS):\e[0m"
    echo "8) Consultar Saldo e Navios de Carteira"
    echo "9) Realizar Transferência de ELIS"
    echo "10) Registrar Nova Empresa (Ganhe 1000 ELIS)"
    echo ""
    echo -e "\e[1;32mCiclo de Vida de Navios (Vessels):\e[0m"
    echo "11) Criar/Subir Novo Navio (Container Docker)"
    echo "12) Abater/Destruir Navio Ativo (Container Docker)"
    echo "13) Parar/Remover um Container Específico"
    echo "0) Sair"
    echo ""
    read -p "Opção: " OPTION

    case $OPTION in
        1)
            echo -e "\n\e[1;32m=> Iniciando o Broker Líder...\e[0m"
            python3 code/generate_dynamic.py --mode lider
            $DOCKER_COMPOSE -f docker-compose-temp.yml up -d --build
            echo -e "\n\e[1;32m[SUCESSO] Líder rodando! O IP deste Líder para os outros PCs é: \e[1;37m$LOCAL_IP\e[0m"
            read -p "Pressione Enter para continuar..."
            ;;
        2)
            echo -e "\n\e[1;32m=> Brokers Adicionais (B2-B4)\e[0m"
            read -p "Qual é o IP do Líder? (Deixe em branco para usar $LOCAL_IP): " LIDER_IP
            LIDER_IP=${LIDER_IP:-$LOCAL_IP}
            read -p "Quantos brokers você quer subir neste PC? (1 a 3): " COUNT
            SUGGESTED_START=$(python3 -c "import sys; sys.path.append('code'); from generate_dynamic import sugerir_start; print(sugerir_start('brokers', $COUNT, '$LIDER_IP'))" 2>/dev/null || echo 2)
            read -p "A partir de qual ID/broker iniciar? (2 a 4, padrão $SUGGESTED_START): " START
            START=${START:-$SUGGESTED_START}
            python3 code/generate_dynamic.py --mode brokers --count "$COUNT" --lider "$LIDER_IP" --start "$START"
            $DOCKER_COMPOSE -f docker-compose-temp.yml up -d --build
            echo -e "\n\e[1;32m[SUCESSO] Subidos $COUNT brokers a partir de B$START apontando para o Líder $LIDER_IP!\e[0m"
            read -p "Pressione Enter para continuar..."
            ;;
        3)
            echo -e "\n\e[1;32m=> Monitor (Dashboard & Explorer)\e[0m"
            read -p "Qual é o IP do Líder? (Deixe em branco para usar $LOCAL_IP): " LIDER_IP
            LIDER_IP=${LIDER_IP:-$LOCAL_IP}
            python3 code/generate_dynamic.py --mode monitor --lider "$LIDER_IP"
            $DOCKER_COMPOSE -f docker-compose-temp.yml up -d --build
            echo -e "\n\e[1;32m[SUCESSO] Monitor rodando! Acesse:\e[0m"
            echo -e "   - Dashboard Principal: http://localhost:8085"
            echo -e "   - Blockchain Explorer: http://localhost:8086"
            read -p "Pressione Enter para continuar..."
            ;;
        4)
            echo -e "\n\e[1;32m=> Drones\e[0m"
            read -p "Qual é o IP do Líder? (Deixe em branco para usar $LOCAL_IP): " LIDER_IP
            LIDER_IP=${LIDER_IP:-$LOCAL_IP}
            read -p "Quantos Drones você quer subir?: " COUNT
            SUGGESTED_START=$(python3 -c "import sys; sys.path.append('code'); from generate_dynamic import sugerir_start; print(sugerir_start('drones', $COUNT, '$LIDER_IP'))" 2>/dev/null || echo 1)
            read -p "A partir de qual ID/índice de drone iniciar? (padrão $SUGGESTED_START): " START
            START=${START:-$SUGGESTED_START}
            python3 code/generate_dynamic.py --mode drones --count "$COUNT" --lider "$LIDER_IP" --start "$START"
            $DOCKER_COMPOSE -f docker-compose-temp.yml up -d --build
            echo -e "\n\e[1;32m[SUCESSO] Subidos $COUNT Drones a partir do Drone $START!\e[0m"
            read -p "Pressione Enter para continuar..."
            ;;
        5)
            echo -e "\n\e[1;32m=> Sensores (2 por setor)\e[0m"
            read -p "Qual é o IP do Líder? (Deixe em branco para usar $LOCAL_IP): " LIDER_IP
            LIDER_IP=${LIDER_IP:-$LOCAL_IP}
            SUGGESTED_START=$(python3 -c "import sys; sys.path.append('code'); from generate_dynamic import sugerir_start; print(sugerir_start('sensores', 1, '$LIDER_IP'))" 2>/dev/null || echo 1)
            read -p "A partir de qual broker/setor deseja cobrir com sensores? (1 a 4, padrão $SUGGESTED_START): " START
            START=${START:-$SUGGESTED_START}
            read -p "Quantos setores (brokers) a partir de B$START você quer cobrir com sensores? (1 a 4): " COUNT
            python3 code/generate_dynamic.py --mode sensores --count "$COUNT" --lider "$LIDER_IP" --start "$START"
            $DOCKER_COMPOSE -f docker-compose-temp.yml up -d --build
            echo -e "\n\e[1;32m[SUCESSO] Sensores criados a partir do setor B$START!\e[0m"
            read -p "Pressione Enter para continuar..."
            ;;
        6)
            echo -e "\n\e[1;32m=> Subindo simulação completa via compose...\e[0m"
            $DOCKER_COMPOSE -f docker-compose-all.yml up -d --build
            echo -e "\n\e[1;32m[SUCESSO] Todos os componentes (B1-B4, Monitor, 8 Drones, 8 Sensores, 4 Navios) iniciados!\e[0m"
            echo -e "Dashboard: http://localhost:8085"
            echo -e "Explorer:  http://localhost:8086"
            read -p "Pressione Enter para continuar..."
            ;;
        7)
            echo -e "\n\e[1;31m=> Parando e removendo todos os containers...\e[0m"
            $DOCKER_COMPOSE -f docker-compose-all.yml down --remove-orphans || true
            $DOCKER_COMPOSE -f docker-compose-escala.yml down --remove-orphans || true
            $DOCKER_COMPOSE -f docker-compose-minimal.yml down --remove-orphans || true
            if [ -f "docker-compose-dist.yml" ]; then
                $DOCKER_COMPOSE -f docker-compose-dist.yml down --remove-orphans || true
            fi
            if [ -f "docker-compose-temp.yml" ]; then
                $DOCKER_COMPOSE -f docker-compose-temp.yml down --remove-orphans || true
                rm -f docker-compose-temp.yml
            fi
            
            # Remove qualquer container residual do projeto
            CONTAINERS_NET=$(docker ps -a --filter "name=hormuznet_" -q)
            if [ -n "$CONTAINERS_NET" ]; then
                echo "Removendo contêineres HormuzNet..."
                docker stop $CONTAINERS_NET || true
                docker rm $CONTAINERS_NET || true
            fi
            CONTAINERS_CHAIN=$(docker ps -a --filter "name=hormuzchain_" -q)
            if [ -n "$CONTAINERS_CHAIN" ]; then
                echo "Removendo contêineres HormuzChain..."
                docker stop $CONTAINERS_CHAIN || true
                docker rm $CONTAINERS_CHAIN || true
            fi
            
            echo -e "\n\e[1;32m[SUCESSO] Ambiente limpo!\e[0m"
            read -p "Pressione Enter para continuar..."
            ;;
        8)
            echo -e "\n\e[1;35m=> Consultar Saldo e Navios de Carteira\e[0m"
            read -p "Nome da empresa (ex: Maersk, MSC, CMA_CGM, Hapag_Lloyd) ou Endereço da carteira: " WALLET_IDENT
            if [[ "$WALLET_IDENT" =~ ^0x ]]; then
                ./hormuz_cli balance -addr "$WALLET_IDENT" -broker "http://localhost:7000"
            else
                ./hormuz_cli balance -company "$WALLET_IDENT" -broker "http://localhost:7000"
            fi
            read -p "Pressione Enter para continuar..."
            ;;
        9)
            echo -e "\n\e[1;35m=> Realizar Transferência de ELIS\e[0m"
            read -p "Empresa remetente (ex: Maersk, MSC, CMA_CGM, Hapag_Lloyd): " FROM_COMP
            read -p "Empresa destinatária (ex: MSC) ou endereço da carteira (0x...): " TO_COMP
            read -p "Quantidade de ELIS: " TRANSFER_AMOUNT
            ./hormuz_cli transfer -from "$FROM_COMP" -to "$TO_COMP" -amount "$TRANSFER_AMOUNT" -broker "http://localhost:7000"
            read -p "Pressione Enter para continuar..."
            ;;
        10)
            echo -e "\n\e[1;35m=> Registrar Nova Empresa\e[0m"
            read -p "Nome da nova empresa: " REG_NAME
            ./hormuz_cli register -name "$REG_NAME" -broker "http://localhost:7000"
            read -p "Pressione Enter para continuar..."
            ;;
        11)
            echo -e "\n\e[1;32m=> Criar/Subir Novo Navio (Container Docker)\e[0m"
            read -p "ID do navio (ex: vessel_maersk_02): " NEW_VESSEL_ID
            
            if [ -z "$NEW_VESSEL_ID" ]; then
                echo -e "\e[1;31mID do navio inválido!\e[0m"
                read -p "Pressione Enter para continuar..."
                continue
            fi

            # Buscar empresas registradas na blockchain (padrão + dinâmicas)
            echo "Buscando empresas registradas na rede..."
            mapfile -t COMPANIES < <(python3 -c "
import urllib.request, json
companies = ['Maersk', 'MSC', 'CMA_CGM', 'Hapag_Lloyd', 'ONE']
for port in [7000, 7001, 7002, 7003]:
    try:
        req = urllib.request.Request(f'http://localhost:{port}/blockchain/transactions')
        with urllib.request.urlopen(req, timeout=1) as response:
            txs = json.loads(response.read().decode())
            for tx in txs:
                if tx.get('type') == 'REGISTER' and tx.get('payload'):
                    name = tx['payload']
                    if name not in companies:
                        companies.append(name)
            break
    except Exception:
        pass
for c in companies:
    print(c)
" 2>/dev/null)

            echo -e "\nDeseja adicionar esse navio à frota de qual empresa?"
            for i in "${!COMPANIES[@]}"; do
                echo "$((i+1))) ${COMPANIES[$i]}"
            done
            
            read -p "Escolha a empresa (número): " COMP_INDEX
            if [[ ! "$COMP_INDEX" =~ ^[0-9]+$ ]] || [ "$COMP_INDEX" -lt 1 ] || [ "$COMP_INDEX" -gt "${#COMPANIES[@]}" ]; then
                echo -e "\e[1;31mOpção inválida! Cancelando registro de navio.\e[0m"
                read -p "Pressione Enter para continuar..."
                continue
            fi
            
            COMP_NAME="${COMPANIES[$((COMP_INDEX-1))]}"
            echo "Empresa selecionada: $COMP_NAME"

            # Obter chaves automaticamente do CLI
            KEYS_OUT=$(./hormuz_cli keys -company "$COMP_NAME" 2>/dev/null)
            if [ -z "$KEYS_OUT" ]; then
                echo -e "\e[1;31mErro ao derivar as chaves da empresa $COMP_NAME!\e[0m"
                read -p "Pressione Enter para continuar..."
                continue
            fi
            COMP_PRIV=$(echo "$KEYS_OUT" | awk '{print $1}')
            COMP_ADDR=$(echo "$KEYS_OUT" | awk '{print $2}')

            # Coordenadas aleatórias
            VESSEL_X=$((100 + RANDOM % 801))
            VESSEL_Y=$((100 + RANDOM % 801))
            echo "Coordenadas geradas automaticamente: X=$VESSEL_X, Y=$VESSEL_Y"

            # Descobrir porta ativa da API REST para registro
            ACTIVE_BROKER_API="http://localhost:7000"
            for port in 7000 7001 7002 7003; do
                if curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/occurrences" --max-time 1 | grep -q "200" 2>/dev/null; then
                    ACTIVE_BROKER_API="http://localhost:${port}"
                    break
                fi
            done

            echo -e "\n=> Registrando navio $NEW_VESSEL_ID na Blockchain via $ACTIVE_BROKER_API..."
            ./hormuz_cli vessel-reg -company "$COMP_NAME" -vessel "$NEW_VESSEL_ID" -broker "$ACTIVE_BROKER_API"
            
            echo -e "=> Construindo imagem docker do navio se necessário..."
            docker build -t hormuznet-vessel -f code/Dockerfile.vessel code
            
            echo -e "=> Iniciando container do navio..."
            docker run -d --network host --name "hormuzchain_${NEW_VESSEL_ID}" \
                -e VESSEL_ID="$NEW_VESSEL_ID" \
                -e COMPANY_NAME="$COMP_NAME" \
                -e COMPANY_ADDR="$COMP_ADDR" \
                -e COMPANY_PRIV_KEY="$COMP_PRIV" \
                -e BROKER_API="$ACTIVE_BROKER_API" \
                -e X="$VESSEL_X" \
                -e Y="$VESSEL_Y" \
                hormuznet-vessel

            echo -e "\n\e[1;32m[SUCESSO] Container docker hormuzchain_${NEW_VESSEL_ID} iniciado com sucesso!\e[0m"
            read -p "Pressione Enter para continuar..."
            ;;
        12)
            echo -e "\n\e[1;32m=> Abater/Destruir Navio Ativo (Container Docker)\e[0m"
            echo "Navios rodando atualmente:"
            docker ps --filter "name=hormuzchain_" --format "table {{.Names}}\t{{.Status}}"
            echo ""
            read -p "Digite o nome completo do container do navio a ser removido: " COMP_VESSEL_CONTAINER
            if [ -n "$COMP_VESSEL_CONTAINER" ]; then
                echo -e "=> Removendo o container $COMP_VESSEL_CONTAINER..."
                docker stop "$COMP_VESSEL_CONTAINER" && docker rm "$COMP_VESSEL_CONTAINER"
                echo -e "\e[1;32m[SUCESSO] Navio removido com sucesso!\e[0m"
            else
                echo "Nenhum container informado."
            fi
            read -p "Pressione Enter para continuar..."
            ;;
        13)
            echo -e "\n\e[1;32m=> Parar/Remover um Container Específico\e[0m"
            echo "Buscando contêineres do HormuzChain..."
            
            mapfile -t ALL_CONTAINER_NAMES < <(docker ps -a --format "{{.Names}}" | grep -E "hormuznet_|hormuzchain_" || true)
            
            if [ ${#ALL_CONTAINER_NAMES[@]} -eq 0 ]; then
                echo "Nenhum contêiner do HormuzChain/HormuzNet encontrado."
                read -p "Pressione Enter para continuar..."
                continue
            fi
            
            echo "Contêineres encontrados:"
            for i in "${!ALL_CONTAINER_NAMES[@]}"; do
                STATUS=$(docker inspect --format='{{.State.Status}}' "${ALL_CONTAINER_NAMES[$i]}" 2>/dev/null || echo "desconhecido")
                echo "$((i+1))) ${ALL_CONTAINER_NAMES[$i]} [Status: $STATUS]"
            done
            echo ""
            read -p "Escolha o número do contêiner que deseja parar e remover (ou 0 para cancelar): " CHOICE
            
            if [[ ! "$CHOICE" =~ ^[0-9]+$ ]] || [ "$CHOICE" -lt 1 ] || [ "$CHOICE" -gt "${#ALL_CONTAINER_NAMES[@]}" ]; then
                if [ "$CHOICE" = "0" ]; then
                    echo "Operação cancelada."
                else
                    echo -e "\e[1;31mOpção inválida!\e[0m"
                fi
            else
                TARGET_CONTAINER="${ALL_CONTAINER_NAMES[$((CHOICE-1))]}"
                echo -e "=> Parando e removendo contêiner: $TARGET_CONTAINER..."
                docker stop "$TARGET_CONTAINER" || true
                docker rm "$TARGET_CONTAINER" || true
                echo -e "\e[1;32m[SUCESSO] Contêiner removido com sucesso!\e[0m"
            fi
            read -p "Pressione Enter para continuar..."
            ;;
        0)
            echo "Saindo..."
            exit 0
            ;;
        *)
            echo -e "\e[1;31mOpção inválida!\e[0m"
            sleep 1
            ;;
    esac
done

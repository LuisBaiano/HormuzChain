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
            if [ -f "docker-compose-temp.yml" ]; then
                $DOCKER_COMPOSE -f docker-compose-temp.yml down --remove-orphans || true
                rm -f docker-compose-temp.yml
            fi
            # Remove navios dinâmicos
            DYN_VESSELS=$(docker ps -a --filter "name=hormuzchain_" -q)
            if [ -n "$DYN_VESSELS" ]; then
                echo "Parando navios dinâmicos..."
                docker stop $DYN_VESSELS || true
                docker rm $DYN_VESSELS || true
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
            read -p "Empresa proprietária (ex: Maersk, MSC, CMA_CGM, Hapag_Lloyd): " COMP_NAME
            read -p "Coordenada X inicial (100-900): " VESSEL_X
            read -p "Coordenada Y inicial (100-900): " VESSEL_Y

            if [ "$COMP_NAME" == "Maersk" ]; then
                COMP_ADDR="0x280f33adb69caa3e5c8c"
                COMP_PRIV="9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4"
            elif [ "$COMP_NAME" == "MSC" ]; then
                COMP_ADDR="0x2a9621c924cf329f550a"
                COMP_PRIV="cb93bbfc68a40806d3b48a97e60faa0df6959b127e677bb175a55267a73c5e20"
            elif [ "$COMP_NAME" == "CMA_CGM" ] || [ "$COMP_NAME" == "CMA" ]; then
                COMP_NAME="CMA_CGM"
                COMP_ADDR="0x7daccdb0e3eb3ce3d768"
                COMP_PRIV="682ea73bd47e2d0c34856edeb0b10de26f00e47ae654ff04d1a4e580fbcaede7"
            elif [ "$COMP_NAME" == "Hapag_Lloyd" ] || [ "$COMP_NAME" == "Hapag" ]; then
                COMP_NAME="Hapag_Lloyd"
                COMP_ADDR="0xf7d808577df8b4454e18"
                COMP_PRIV="303b1fa143cff773c69665a3891971bde28806c59ea2d4930f9fb8ac2c861a0a"
            else
                read -p "Endereço da carteira da empresa (0x...): " COMP_ADDR
                read -p "Chave privada da empresa: " COMP_PRIV
            fi

            echo -e "\n=> Registrando navio $NEW_VESSEL_ID na Blockchain..."
            ./hormuz_cli vessel-reg -company "$COMP_NAME" -vessel "$NEW_VESSEL_ID" -broker "http://localhost:7000"
            
            echo -e "=> Construindo imagem docker do navio se necessário..."
            docker build -t hormuznet-vessel -f code/Dockerfile.vessel code
            
            echo -e "=> Iniciando container do navio..."
            docker run -d --network host --name "hormuzchain_${NEW_VESSEL_ID}" \
                -e VESSEL_ID="$NEW_VESSEL_ID" \
                -e COMPANY_NAME="$COMP_NAME" \
                -e COMPANY_ADDR="$COMP_ADDR" \
                -e COMPANY_PRIV_KEY="$COMP_PRIV" \
                -e BROKER_API="http://localhost:7000" \
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

#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
# HormuzChain — subir_distribuido.sh
# Script para configurar e rodar a simulação distribuída em até 4 PCs.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# Cores para o terminal
GREEN="\e[1;32m"
YELLOW="\e[1;33m"
RED="\e[1;31m"
BLUE="\e[1;34m"
RESET="\e[0m"

# Detecta docker compose
if docker compose version &>/dev/null 2>&1; then
    DOCKER_COMPOSE="docker compose"
elif command -v docker-compose &>/dev/null; then
    DOCKER_COMPOSE="docker-compose"
else
    echo -e "${RED}[ERRO] Docker Compose não encontrado! Instale-o para continuar.${RESET}"
    exit 1
fi

# Pega o IP local
LOCAL_IP=$(hostname -I | awk '{print $1}')

clear
echo -e "${BLUE}╔══════════════════════════════════════════════════════════╗"
echo -e "║           HormuzChain — Inicialização Multi-PC           ║"
echo -e "╚══════════════════════════════════════════════════════════╝${RESET}\n"

echo -e "${YELLOW}Selecione a função deste computador:${RESET}"
echo "1) PC 1 - Broker Líder (B1) + Monitor + Frota Noroeste"
echo "2) PC 2 - Broker Seguidor (B2) + Frota Nordeste"
echo "3) PC 3 - Broker Seguidor (B3) + Frota Sudoeste"
echo "4) PC 4 - Broker Seguidor (B4) + Frota Sudeste"
echo ""
read -p "Opção (1-4): " OPTION

if [[ ! "$OPTION" =~ ^[1-4]$ ]]; then
    echo -e "${RED}[ERRO] Opção inválida! Encerrando.${RESET}"
    exit 1
fi

PC_ID=$OPTION

if [ "$PC_ID" -eq 1 ]; then
    LIDER_IP="127.0.0.1"
else
    read -p "Digite o último octeto (X) do IP do Líder (172.16.103.X): " LIDER_X
    if [ -z "$LIDER_X" ]; then
        echo -e "${RED}[ERRO] O final do IP (X) do Líder é obrigatório!${RESET}"
        exit 1
    fi
    LIDER_IP="172.16.103.$LIDER_X"
fi

echo -e "\n${YELLOW}=> Gerando configuração docker-compose-dist.yml para o PC $PC_ID...${RESET}"

# Gera o docker-compose baseado no ID do PC
if [ "$PC_ID" -eq 1 ]; then
    cat << EOF > docker-compose-dist.yml
version: '3.8'
services:
  broker1:
    build: { context: ./code, dockerfile: Dockerfile.broker }
    container_name: hormuznet_broker1
    network_mode: host
    command: ["-id=B1", "-setor=Setor_Noroeste", "-udp=224.1.2.3:9876", "-tcp=0.0.0.0:6000"]
    environment: { ENABLE_TOKEN_REPLENISHMENT: 'true' }
    restart: on-failure

  monitor:
    build: { context: ./code, dockerfile: Dockerfile.monitor }
    container_name: hormuznet_monitor
    network_mode: host
    command: ["-brokers=127.0.0.1:6000", "-porta=8085", "-explorer-porta=8086"]
    restart: on-failure

  drone1:
    build: { context: ./code, dockerfile: Dockerfile.drone }
    container_name: hormuznet_drone_nw_1
    network_mode: host
    command: ["-id=Drone_NW_1", "-brokers=127.0.0.1:6000", "-x=250", "-y=750"]
    restart: on-failure

  sensor_1:
    build: { context: ./code, dockerfile: Dockerfile.sensor }
    container_name: hormuznet_sonar_noroeste_1
    network_mode: host
    command: ["-id=sonar_noroeste_1", "-tipo=sonar", "-setor=Setor_Noroeste", "-broker=224.1.2.3:9876", "-intervalo=15000", "-x=150", "-y=750"]
    restart: on-failure

  vessel_1:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_maersk_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_maersk_01
      COMPANY_NAME: Maersk
      COMPANY_ADDR: '0x280f33adb69caa3e5c8c'
      COMPANY_PRIV_KEY: 9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4
      BROKER_API: http://localhost:7000
      X: '120'
      Y: '780'
    restart: on-failure

  vessel_2:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_maersk_02
    network_mode: host
    environment:
      VESSEL_ID: vessel_maersk_02
      COMPANY_NAME: Maersk
      COMPANY_ADDR: '0x280f33adb69caa3e5c8c'
      COMPANY_PRIV_KEY: 9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4
      BROKER_API: http://localhost:7000
      X: '220'
      Y: '820'
    restart: on-failure

  vessel_9:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_one_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_one_01
      COMPANY_NAME: ONE
      COMPANY_ADDR: '0x371273902bfb4590c1d5'
      COMPANY_PRIV_KEY: 2192e8955d5e1ad1651f2f0c637e6f1ac82855747a5f42f978db28669595dc21
      BROKER_API: http://localhost:7000
      X: '450'
      Y: '500'
    restart: on-failure
EOF

elif [ "$PC_ID" -eq 2 ]; then
    cat << EOF > docker-compose-dist.yml
version: '3.8'
services:
  broker2:
    build: { context: ./code, dockerfile: Dockerfile.broker }
    container_name: hormuznet_broker2
    network_mode: host
    command: ["-id=B2", "-setor=Setor_Nordeste", "-udp=224.1.2.3:9876", "-tcp=0.0.0.0:6001", "-lider=${LIDER_IP}:6000"]
    restart: on-failure

  drone2:
    build: { context: ./code, dockerfile: Dockerfile.drone }
    container_name: hormuznet_drone_ne_1
    network_mode: host
    command: ["-id=Drone_NE_1", "-brokers=${LIDER_IP}:6000", "-x=750", "-y=750"]
    restart: on-failure

  sensor_2:
    build: { context: ./code, dockerfile: Dockerfile.sensor }
    container_name: hormuznet_radar_nordeste_1
    network_mode: host
    command: ["-id=radar_nordeste_1", "-tipo=radar", "-setor=Setor_Nordeste", "-broker=224.1.2.3:9876", "-intervalo=15000", "-x=650", "-y=750"]
    restart: on-failure

  vessel_3:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_msc_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_msc_01
      COMPANY_NAME: MSC
      COMPANY_ADDR: '0x2a9621c924cf329f550a'
      COMPANY_PRIV_KEY: cb93bbfc68a40806d3b48a97e60faa0df6959b127e677bb175a55267a73c5e20
      BROKER_API: http://localhost:7001
      X: '620'
      Y: '780'
    restart: on-failure

  vessel_4:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_msc_02
    network_mode: host
    environment:
      VESSEL_ID: vessel_msc_02
      COMPANY_NAME: MSC
      COMPANY_ADDR: '0x2a9621c924cf329f550a'
      COMPANY_PRIV_KEY: cb93bbfc68a40806d3b48a97e60faa0df6959b127e677bb175a55267a73c5e20
      BROKER_API: http://localhost:7001
      X: '720'
      Y: '820'
    restart: on-failure

  vessel_10:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_one_02
    network_mode: host
    environment:
      VESSEL_ID: vessel_one_02
      COMPANY_NAME: ONE
      COMPANY_ADDR: '0x371273902bfb4590c1d5'
      COMPANY_PRIV_KEY: 2192e8955d5e1ad1651f2f0c637e6f1ac82855747a5f42f978db28669595dc21
      BROKER_API: http://localhost:7001
      X: '550'
      Y: '500'
    restart: on-failure
EOF

elif [ "$PC_ID" -eq 3 ]; then
    cat << EOF > docker-compose-dist.yml
version: '3.8'
services:
  broker3:
    build: { context: ./code, dockerfile: Dockerfile.broker }
    container_name: hormuznet_broker3
    network_mode: host
    command: ["-id=B3", "-setor=Setor_Sudoeste", "-udp=224.1.2.3:9876", "-tcp=0.0.0.0:6002", "-lider=${LIDER_IP}:6000"]
    restart: on-failure

  drone3:
    build: { context: ./code, dockerfile: Dockerfile.drone }
    container_name: hormuznet_drone_sw_1
    network_mode: host
    command: ["-id=Drone_SW_1", "-brokers=${LIDER_IP}:6000", "-x=250", "-y=250"]
    restart: on-failure

  sensor_3:
    build: { context: ./code, dockerfile: Dockerfile.sensor }
    container_name: hormuznet_boia_sudoeste_1
    network_mode: host
    command: ["-id=boia_sudoeste_1", "-tipo=boia", "-setor=Setor_Sudoeste", "-broker=224.1.2.3:9876", "-intervalo=15000", "-x=150", "-y=250"]
    restart: on-failure

  vessel_5:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_cma_cgm_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_cma_cgm_01
      COMPANY_NAME: CMA_CGM
      COMPANY_ADDR: '0x7daccdb0e3eb3ce3d768'
      COMPANY_PRIV_KEY: 682ea73bd47e2d0c34856edeb0b10de26f00e47ae654ff04d1a4e580fbcaede7
      BROKER_API: http://localhost:7002
      X: '120'
      Y: '280'
    restart: on-failure

  vessel_6:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_cma_cgm_02
    network_mode: host
    environment:
      VESSEL_ID: vessel_cma_cgm_02
      COMPANY_NAME: CMA_CGM
      COMPANY_ADDR: '0x7daccdb0e3eb3ce3d768'
      COMPANY_PRIV_KEY: 682ea73bd47e2d0c34856edeb0b10de26f00e47ae654ff04d1a4e580fbcaede7
      BROKER_API: http://localhost:7002
      X: '220'
      Y: '320'
    restart: on-failure
EOF

elif [ "$PC_ID" -eq 4 ]; then
    cat << EOF > docker-compose-dist.yml
version: '3.8'
services:
  broker4:
    build: { context: ./code, dockerfile: Dockerfile.broker }
    container_name: hormuznet_broker4
    network_mode: host
    command: ["-id=B4", "-setor=Setor_Sudeste", "-udp=224.1.2.3:9876", "-tcp=0.0.0.0:6003", "-lider=${LIDER_IP}:6000"]
    restart: on-failure

  drone4:
    build: { context: ./code, dockerfile: Dockerfile.drone }
    container_name: hormuznet_drone_se_1
    network_mode: host
    command: ["-id=Drone_SE_1", "-brokers=${LIDER_IP}:6000", "-x=750", "-y=250"]
    restart: on-failure

  sensor_4:
    build: { context: ./code, dockerfile: Dockerfile.sensor }
    container_name: hormuznet_visual_sudeste_1
    network_mode: host
    command: ["-id=visual_sudeste_1", "-tipo=visual", "-setor=Setor_Sudeste", "-broker=224.1.2.3:9876", "-intervalo=15000", "-x=650", "-y=250"]
    restart: on-failure

  vessel_7:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_hapag_lloyd_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_hapag_lloyd_01
      COMPANY_NAME: Hapag_Lloyd
      COMPANY_ADDR: '0xf7d808577df8b4454e18'
      COMPANY_PRIV_KEY: 303b1fa143cff773c69665a3891971bde28806c59ea2d4930f9fb8ac2c861a0a
      BROKER_API: http://localhost:7003
      X: '620'
      Y: '280'
    restart: on-failure

  vessel_8:
    build: { context: ./code, dockerfile: Dockerfile.vessel }
    container_name: hormuzchain_vessel_hapag_lloyd_02
    network_mode: host
    environment:
      VESSEL_ID: vessel_hapag_lloyd_02
      COMPANY_NAME: Hapag_Lloyd
      COMPANY_ADDR: '0xf7d808577df8b4454e18'
      COMPANY_PRIV_KEY: 303b1fa143cff773c69665a3891971bde28806c59ea2d4930f9fb8ac2c861a0a
      BROKER_API: http://localhost:7003
      X: '720'
      Y: '320'
    restart: on-failure
EOF
fi

echo -e "\n${YELLOW}=> Compilando e iniciando contêineres do PC $PC_ID...${RESET}"
$DOCKER_COMPOSE -f docker-compose-dist.yml up --build -d

echo -e "\n${GREEN}[SUCESSO] Simulação iniciada para este PC!${RESET}"

if [ "$PC_ID" -eq 1 ]; then
    X_VAL="X"
    if [[ "$LOCAL_IP" =~ ^172\.16\.103\.([0-9]+)$ ]]; then
        X_VAL="${BASH_REMATCH[1]}"
    fi
    echo -e "--------------------------------------------------------"
    echo -e "  PC 1 (LÍDER) iniciado com sucesso!"
    echo -e "  Dashboard: http://localhost:8085"
    echo -e "  Explorer:  http://localhost:8086"
    echo -e "  IP do Líder para os outros PCs: ${YELLOW}${LOCAL_IP}${RESET}"
    if [ "$X_VAL" != "X" ]; then
        echo -e "  Valor de X para digitar nos outros PCs: ${GREEN}${X_VAL}${RESET}"
    fi
    echo -e "--------------------------------------------------------"
else
    echo -e "--------------------------------------------------------"
    echo -e "  PC $PC_ID (SEGUIDOR) conectado com sucesso!"
    echo -e "  Líder em: $LIDER_IP"
    echo -e "--------------------------------------------------------"
fi

echo -e "\nPara parar os containers deste PC, execute:"
echo -e "  $DOCKER_COMPOSE -f docker-compose-dist.yml down --remove-orphans"

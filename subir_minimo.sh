#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
# HormuzChain — subir_minimo.sh
# Script para gerar e subir um ambiente de testes MÍNIMO funcional.
# Mantém o quórum de consenso PoA (3 de 4 validadores) rodando apenas B1, B2 e B3.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# Detecta docker compose
if docker compose version &>/dev/null 2>&1; then
    DOCKER_COMPOSE="docker compose"
elif command -v docker-compose &>/dev/null; then
    DOCKER_COMPOSE="docker-compose"
else
    echo -e "\e[1;31m[ERRO] Docker Compose não encontrado!\e[0m"
    exit 1
fi

echo -e "\e[1;33m=> Criando configuração docker-compose-minimal.yml...\e[0m"

cat <<EOF > docker-compose-minimal.yml
version: '3.8'
services:
  broker1:
    build:
      context: ./code
      dockerfile: Dockerfile.broker
    container_name: hormuznet_broker1
    network_mode: host
    command:
    - -id=B1
    - -setor=Setor_Noroeste
    - -udp=224.1.2.3:9876
    - -tcp=0.0.0.0:6000
    environment:
      ENABLE_TOKEN_REPLENISHMENT: "true"
    restart: on-failure

  broker2:
    build:
      context: ./code
      dockerfile: Dockerfile.broker
    container_name: hormuznet_broker2
    network_mode: host
    command:
    - -id=B2
    - -setor=Setor_Nordeste
    - -udp=224.1.2.3:9876
    - -tcp=0.0.0.0:6001
    - -lider=127.0.0.1:6000
    restart: on-failure

  broker3:
    build:
      context: ./code
      dockerfile: Dockerfile.broker
    container_name: hormuznet_broker3
    network_mode: host
    command:
    - -id=B3
    - -setor=Setor_Sudoeste
    - -udp=224.1.2.3:9876
    - -tcp=0.0.0.0:6002
    - -lider=127.0.0.1:6000
    restart: on-failure

  monitor:
    build:
      context: ./code
      dockerfile: Dockerfile.monitor
    container_name: hormuznet_monitor
    network_mode: host
    command:
    - -brokers=127.0.0.1:6000
    - -porta=8085
    restart: on-failure

  drone1:
    build:
      context: ./code
      dockerfile: Dockerfile.drone
    container_name: hormuznet_drone_nw_1
    network_mode: host
    command:
    - -id=Drone_NW_1
    - -brokers=127.0.0.1:6000,127.0.0.1:6001,127.0.0.1:6002
    - -x=250
    - -y=750
    restart: on-failure

  sensor_1:
    build:
      context: ./code
      dockerfile: Dockerfile.sensor
    container_name: hormuznet_sonar_noroeste_1
    network_mode: host
    command:
    - -id=sonar_noroeste_1
    - -tipo=sonar
    - -setor=Setor_Noroeste
    - -broker=224.1.2.3:9876
    - -intervalo=20000
    - -x=150
    - -y=750
    - -broker-api=http://localhost:7000,http://localhost:7001,http://localhost:7002,http://localhost:7003
    restart: on-failure

  vessel_1:
    build:
      context: ./code
      dockerfile: Dockerfile.vessel
    container_name: hormuzchain_vessel_maersk_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_maersk_01
      COMPANY_NAME: Maersk
      COMPANY_ADDR: '0x280f33adb69caa3e5c8c'
      COMPANY_PRIV_KEY: 9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4
      BROKER_API: http://localhost:7000,http://localhost:7001,http://localhost:7002
      X: '150'
      Y: '750'
    restart: on-failure

  vessel_2:
    build:
      context: ./code
      dockerfile: Dockerfile.vessel
    container_name: hormuzchain_vessel_maersk_02
    network_mode: host
    environment:
      VESSEL_ID: vessel_maersk_02
      COMPANY_NAME: Maersk
      COMPANY_ADDR: '0x280f33adb69caa3e5c8c'
      COMPANY_PRIV_KEY: 9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4
      BROKER_API: http://localhost:7000,http://localhost:7001,http://localhost:7002
      X: '250'
      Y: '800'
    restart: on-failure

  vessel_3:
    build:
      context: ./code
      dockerfile: Dockerfile.vessel
    container_name: hormuzchain_vessel_msc_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_msc_01
      COMPANY_NAME: MSC
      COMPANY_ADDR: '0x2a9621c924cf329f550a'
      COMPANY_PRIV_KEY: cb93bbfc68a40806d3b48a97e60faa0df6959b127e677bb175a55267a73c5e20
      BROKER_API: http://localhost:7001,http://localhost:7000,http://localhost:7002
      X: '650'
      Y: '750'
    restart: on-failure

  vessel_4:
    build:
      context: ./code
      dockerfile: Dockerfile.vessel
    container_name: hormuzchain_vessel_cma_cgm_01
    network_mode: host
    environment:
      VESSEL_ID: vessel_cma_cgm_01
      COMPANY_NAME: CMA_CGM
      COMPANY_ADDR: '0x7daccdb0e3eb3ce3d768'
      COMPANY_PRIV_KEY: 682ea73bd47e2d0c34856edeb0b10de26f00e47ae654ff04d1a4e580fbcaede7
      BROKER_API: http://localhost:7002,http://localhost:7000,http://localhost:7001
      X: '150'
      Y: '250'
    restart: on-failure
EOF

echo -e "\e[1;32m=> Compilando e iniciando contêineres mínimos (10 contêineres total)...\e[0m"
$DOCKER_COMPOSE -f docker-compose-minimal.yml up -d --build

echo -e "\n\e[1;32m[SUCESSO] Simulação MÍNIMA iniciada!\e[0m"
echo -e "  - Dashboard Unificado: \e[1;36mhttp://localhost:8085\e[0m"
echo -e "\nPara encerrar esta simulação mínima, execute:"
echo -e "  \e[0;90m$DOCKER_COMPOSE -f docker-compose-minimal.yml down --remove-orphans\e[0m"

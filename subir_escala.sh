#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
# HormuzChain — subir_escala.sh
# Script para gerar e subir um ambiente em escala (10 navios, 3 drones, 5 empresas).
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

echo -e "\e[1;33m=> Gerando topologia em escala docker-compose-escala.yml...\e[0m"
python3 generate_scale.py

echo -e "\e[1;33m=> Compilando e iniciando contêineres da escala...\e[0m"
$DOCKER_COMPOSE -f docker-compose-escala.yml up --build -d

echo -e "\e[1;32m[SUCESSO] Simulação EM ESCALA iniciada!\e[0m"
echo -e "  - 4 Brokers (B1-B4) com consenso PoA ativo"
echo -e "  - 3 Drones para escolta (NW, NE, SW)"
echo -e "  - 5 Empresas registradas com 2 navios cada (Total: 10 navios)"
echo -e "  - Dashboard Unificado: http://localhost:8085"
echo -e "  - Blockchain Explorer: http://localhost:8086"
echo -e "\nPara encerrar esta simulação em escala, execute:"
echo -e "  docker-compose -f docker-compose-escala.yml down --remove-orphans"

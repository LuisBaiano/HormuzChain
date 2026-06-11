#!/usr/bin/env bash
# ═══════════════════════════════════════════════════════════════════════════════
# HormuzNet — eliminar.sh
# Script unificado para remover TODOS os contêineres e imagens Docker do sistema.
# INDEPENDENTEMENTE DA APLICAÇÃO.
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# Cores
RED="\e[1;31m"
GREEN="\e[1;32m"
YELLOW="\e[1;33m"
RESET="\e[0m"

echo -e "${RED}[!] ATENÇÃO: Esta ação irá remover TODOS os contêineres e imagens Docker deste computador!${RESET}"
read -p "Deseja continuar? (s/N): " CONFIRM

if [ "$CONFIRM" != "s" ] && [ "$CONFIRM" != "S" ]; then
    echo "Operação cancelada."
    exit 0
fi

echo -e "\n${YELLOW}=> Parando e removendo TODOS os contêineres Docker...${RESET}"
CONTAINERS=$(docker ps -aq)
if [ -n "$CONTAINERS" ]; then
    docker rm -f $CONTAINERS
    echo -e "${GREEN}✓ Todos os contêineres foram removidos.${RESET}"
else
    echo -e "Nenhum contêiner encontrado."
fi

echo -e "\n${YELLOW}=> Removendo TODAS as imagens Docker...${RESET}"
IMAGES=$(docker images -aq)
if [ -n "$IMAGES" ]; then
    docker rmi -f $IMAGES
    echo -e "${GREEN}✓ Todas as imagens Docker foram removidas.${RESET}"
else
    echo -e "Nenhuma imagem encontrada."
fi

# Limpar redes e volumes órfãos
echo -e "\n${YELLOW}=> Limpando resíduos (redes e volumes)...${RESET}"
docker system prune -f --volumes

# Também limpar processos Go órfãos locais
echo -e "\n${YELLOW}=> Limpando possíveis processos Go órfãos do simulador...${RESET}"
pkill -9 -f "go run ./cmd/|.*_main|echo '--- encerrado ---'" 2>/dev/null || true

echo -e "\n${GREEN}[SUCESSO] Todo o ambiente Docker e os processos locais foram completamente limpos!${RESET}"

#!/usr/bin/env bash
# Cleanup faro operator from Kubernetes cluster

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Faro Operator Cleanup Script${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Check if kubectl is configured
if ! kubectl cluster-info &>/dev/null; then
    echo -e "${RED}❌ Error: kubectl not configured or cluster not accessible${NC}"
    exit 1
fi

# Check if operator is deployed
if ! kubectl get namespace faro-system &>/dev/null; then
    echo -e "${YELLOW}⚠️  Faro operator namespace not found. Nothing to clean up.${NC}"
    exit 0
fi

echo -e "${YELLOW}🗑️  Deleting faro operator resources...${NC}"
echo ""

# Delete in reverse order
echo -e "${YELLOW}→ Deleting service...${NC}"
kubectl delete -f deploy/operator/service.yaml --ignore-not-found=true

echo -e "${YELLOW}→ Deleting deployment...${NC}"
kubectl delete -f deploy/operator/deployment.yaml --ignore-not-found=true

echo -e "${YELLOW}→ Deleting configuration...${NC}"
kubectl delete -f deploy/operator/configmap.yaml --ignore-not-found=true

echo -e "${YELLOW}→ Deleting RBAC resources...${NC}"
kubectl delete -f deploy/operator/clusterrolebinding.yaml --ignore-not-found=true
kubectl delete -f deploy/operator/clusterrole.yaml --ignore-not-found=true
kubectl delete -f deploy/operator/serviceaccount.yaml --ignore-not-found=true

echo -e "${YELLOW}→ Deleting namespace...${NC}"
kubectl delete -f deploy/operator/namespace.yaml --ignore-not-found=true

echo ""
echo -e "${GREEN}✅ Faro operator cleanup complete!${NC}"
echo ""


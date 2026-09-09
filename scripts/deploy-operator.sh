#!/usr/bin/env bash
# Deploy faro operator to Kubernetes cluster

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}========================================${NC}"
echo -e "${BLUE}Faro Operator Deployment Script${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Check if we're in the right directory
if [ ! -f "Makefile" ] || [ ! -d "deploy/operator" ]; then
    echo -e "${RED}❌ Error: Must run from project root${NC}"
    exit 1
fi

# Check if kubectl is configured
if ! kubectl cluster-info &>/dev/null; then
    echo -e "${RED}❌ Error: kubectl not configured or cluster not accessible${NC}"
    echo -e "${YELLOW}💡 Tip: Set KUBECONFIG or ensure cluster is running${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Kubernetes cluster accessible${NC}"
kubectl cluster-info | head -1
echo ""

# Check if operator image exists
if ! podman image exists localhost/faro-operator:latest; then
    echo -e "${YELLOW}⚠️  Operator image not found. Building...${NC}"
    make operator-image
fi

echo -e "${GREEN}✅ Operator image available: localhost/faro-operator:latest${NC}"
echo ""

# Deploy using kubectl
echo -e "${BLUE}📦 Deploying faro operator...${NC}"

# Apply manifests in order
echo -e "${YELLOW}→ Creating namespace...${NC}"
kubectl apply -f deploy/operator/namespace.yaml

echo -e "${YELLOW}→ Creating RBAC resources...${NC}"
kubectl apply -f deploy/operator/serviceaccount.yaml
kubectl apply -f deploy/operator/clusterrole.yaml
kubectl apply -f deploy/operator/clusterrolebinding.yaml

echo -e "${YELLOW}→ Creating configuration...${NC}"
kubectl apply -f deploy/operator/configmap.yaml

echo -e "${YELLOW}→ Creating deployment...${NC}"
kubectl apply -f deploy/operator/deployment.yaml

echo -e "${YELLOW}→ Creating service...${NC}"
kubectl apply -f deploy/operator/service.yaml

echo ""
echo -e "${BLUE}⏳ Waiting for deployment to be ready...${NC}"
if kubectl wait --for=condition=available --timeout=120s deployment/faro-operator -n faro-system; then
    echo -e "${GREEN}✅ Deployment is ready!${NC}"
else
    echo -e "${RED}❌ Deployment failed to become ready${NC}"
    echo ""
    echo -e "${YELLOW}Pod status:${NC}"
    kubectl get pods -n faro-system
    echo ""
    echo -e "${YELLOW}Recent events:${NC}"
    kubectl get events -n faro-system --sort-by='.lastTimestamp' | tail -10
    exit 1
fi

echo ""
echo -e "${BLUE}========================================${NC}"
echo -e "${GREEN}✅ Faro Operator Deployed Successfully!${NC}"
echo -e "${BLUE}========================================${NC}"
echo ""

# Show deployment status
echo -e "${BLUE}📊 Deployment Status:${NC}"
kubectl get all -n faro-system
echo ""

# Show pod details
echo -e "${BLUE}🔍 Operator Pod Details:${NC}"
kubectl get pods -n faro-system -o wide
echo ""

# Show recent logs
echo -e "${BLUE}📜 Recent Operator Logs (last 20 lines):${NC}"
kubectl logs -n faro-system deployment/faro-operator --tail=20
echo ""

echo -e "${BLUE}========================================${NC}"
echo -e "${GREEN}Useful Commands:${NC}"
echo -e "${BLUE}========================================${NC}"
echo -e "  ${YELLOW}View logs:${NC}       kubectl logs -n faro-system -f deployment/faro-operator"
echo -e "  ${YELLOW}View events:${NC}     kubectl get events -n faro-system --sort-by='.lastTimestamp'"
echo -e "  ${YELLOW}Check status:${NC}    kubectl get all -n faro-system"
echo -e "  ${YELLOW}View config:${NC}     kubectl get configmap -n faro-system faro-operator-config -o yaml"
echo -e "  ${YELLOW}Delete:${NC}          ./scripts/cleanup-operator.sh"
echo ""


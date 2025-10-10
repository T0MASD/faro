# Migrating from KinD to kinc in GitHub Actions

**Replace KinD with kinc for rootless Kubernetes testing in your CI/CD pipelines**

---

## Quick Migration Guide

### Before: KinD Setup
```yaml
- name: Install KinD  
  run: go install sigs.k8s.io/kind@v0.22.0

- name: Create cluster
  run: |
    kind create cluster
    kubectl cluster-info
    kubectl get nodes
```

### After: kinc Setup  
```yaml
- name: Deploy kinc cluster
  run: |
    # Enable IP forwarding
    echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward
    
    # Start kinc cluster (unprivileged)
    podman run -d --name kinc-cluster \
      --publish 6443:6443 \
      ghcr.io/t0masd/kinc:v1.33.5
    
    # Wait for cluster ready
    timeout 300 bash -c 'until curl -k -s https://127.0.0.1:6443/healthz >/dev/null; do sleep 2; done'
    
    # Extract kubeconfig  
    mkdir -p ~/.kube
    podman cp kinc-cluster:/etc/kubernetes/admin.conf ~/.kube/config
    sed -i 's|server: https://.*:6443|server: https://127.0.0.1:6443|g' ~/.kube/config
    
    # Verify cluster
    kubectl cluster-info
    kubectl get nodes
```

---

## Complete GitHub Actions Workflow

Based on the actual Faro project workflow, here's the minimal migration:

```yaml
name: Test with kinc

on:
  push:
    branches: [ main ]
  pull_request:
    branches: [ main ]

jobs:
  test:
    runs-on: ubuntu-latest
    
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: 1.24

      - name: Deploy kinc cluster
        run: |
          # Enable IP forwarding
          echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward
          
          # Start kinc cluster (unprivileged - no --privileged needed)
          podman run -d --name kinc-cluster \
            --publish 6443:6443 \
            ghcr.io/t0masd/kinc:v1.33.5
          
          # Wait for cluster ready
          timeout 300 bash -c 'until curl -k -s https://127.0.0.1:6443/healthz >/dev/null; do sleep 2; done'
          
          # Extract kubeconfig
          mkdir -p ~/.kube
          podman cp kinc-cluster:/etc/kubernetes/admin.conf ~/.kube/config
          sed -i 's|server: https://.*:6443|server: https://127.0.0.1:6443|g' ~/.kube/config
          
          # Verify cluster is working
          kubectl cluster-info
          kubectl get nodes

      - name: Run tests
        run: |
          go mod tidy
          make test

      - name: Upload test results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: test-results
          path: |
            *.log
            test-results/

      - name: Cleanup
        if: always()
        run: podman rm -f kinc-cluster || true
```

## Key Differences from KinD

| Feature | KinD | kinc |
|---------|------|------|
| **Installation** | `go install sigs.k8s.io/kind@v0.22.0` | Pre-built image |
| **Cluster Creation** | `kind create cluster` | `podman run ghcr.io/t0masd/kinc:v1.33.5` |
| **Privileges** | Unprivileged | Unprivileged |
| **Setup Time** | ~30s | ~90s |
| **Container Runtime** | containerd | CRI-O |

## Benefits of kinc

- ✅ **No binary installation** - Uses pre-built container image
- ✅ **Fully rootless** - Enhanced security  
- ✅ **CRI-O runtime** - Production-grade container runtime
- ✅ **Self-contained** - Everything embedded in one image

## Troubleshooting

### Common Issues

#### Issue: Cluster not ready timeout
**Solution**: Check logs and increase timeout:
```yaml
- name: Debug cluster startup
  if: failure()
  run: |
    podman logs kinc-cluster
    podman exec kinc-cluster systemctl --user status kubelet || true
```

#### Issue: "Permission denied" errors  
**Solution**: Ensure IP forwarding is enabled:
```yaml
- name: Enable IP forwarding
  run: echo 1 | sudo tee /proc/sys/net/ipv4/ip_forward
```

#### Issue: Kubeconfig connection refused
**Solution**: Verify the cluster is actually ready:
```bash
# Check if API server is responding
curl -k -s https://127.0.0.1:6443/healthz

# Check container logs
podman logs kinc-cluster
```

### Getting Help

- **kinc Issues**: https://github.com/T0MASD/kinc/issues
- **Documentation**: https://github.com/T0MASD/kinc/blob/main/README.md

---

## Migration Checklist

- [ ] Replace `go install sigs.k8s.io/kind@v0.22.0` with kinc container
- [ ] Replace `kind create cluster` with `podman run ghcr.io/t0masd/kinc:v1.33.5`
- [ ] Add IP forwarding setup
- [ ] Update kubeconfig extraction
- [ ] Test with your existing test suite
- [ ] Update cleanup to use `podman rm -f kinc-cluster`

---

**Ready to migrate?** The minimal change is just replacing the cluster creation step!

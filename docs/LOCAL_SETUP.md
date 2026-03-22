# AgentRoll 本地开发环境搭建指南

## 一、将这些文件推送到GitHub

你已经有了 github.com/ywc668/agentroll repo。现在把初始文件推上去。

### 方式A：直接在GitHub网页操作（最简单）

1. 打开 https://github.com/ywc668/agentroll
2. 依次创建以下文件（点 Add file → Create new file）：
   - `CONTRIBUTING.md` — 粘贴我提供的内容
   - `api/v1alpha1/agentdeployment_types.go` — CRD核心类型定义
   - `api/v1alpha1/groupversion_info.go` — API组版本信息
   - `config/samples/basic-agent-deployment.yaml` — 完整示例
   - `config/samples/prompt-only-change.yaml` — 轻量示例
   - `docs/adr/001-build-on-argo-rollouts.md` — 架构决策记录
   - `templates/analysis/agent-quality-check.yaml` — 预置分析模板

### 方式B：Clone到本地后推送

```bash
# Clone你的repo
git clone https://github.com/ywc668/agentroll.git
cd agentroll

# 把我生成的文件复制到对应目录（你需要手动创建目录结构）
mkdir -p api/v1alpha1 config/samples templates/analysis docs/adr

# 复制文件后推送
git add .
git commit -m "feat: initial project structure with CRD types and samples"
git push origin main
```

## 二、本地Go开发环境（等新MacBook到了再做也行）

### 安装Go
```bash
# macOS
brew install go

# 验证
go version   # 需要1.22+
```

### 安装Kubebuilder
```bash
# macOS
brew install kubebuilder

# 验证
kubebuilder version
```

### 初始化Kubebuilder项目

重要：因为我们已经手动创建了CRD类型文件，Kubebuilder初始化需要
配合现有代码。推荐的方式是：

```bash
cd agentroll

# 初始化Go module
go mod init github.com/ywc668/agentroll

# 初始化Kubebuilder（会创建项目脚手架）
kubebuilder init --domain agentroll.dev --repo github.com/ywc668/agentroll

# 创建API（会生成controller脚手架）
kubebuilder create api --group agentroll --version v1alpha1 --kind AgentDeployment

# 当它问 "Create Resource [y/n]" → 选 n（我们已经有types.go了）
# 当它问 "Create Controller [y/n]" → 选 y

# 生成CRD manifest和deepcopy方法
make manifests
make generate

# 验证能编译
make build

# 运行测试
make test
```

### 安装本地Kubernetes集群

```bash
# 推荐用kind（轻量，适合开发）
brew install kind

# 创建集群
kind create cluster --name agentroll-dev

# 验证
kubectl cluster-info
```

### 安装Argo Rollouts

```bash
# 安装Argo Rollouts到集群
kubectl create namespace argo-rollouts
kubectl apply -n argo-rollouts -f https://github.com/argoproj/argo-rollouts/releases/latest/download/install.yaml

# 安装kubectl插件（可选，方便调试）
brew install argoproj/tap/kubectl-argo-rollouts

# 验证
kubectl argo rollouts version
```

## 三、开发工作流

### 日常开发循环

```bash
# 1. 修改CRD类型 (api/v1alpha1/agentdeployment_types.go)
# 2. 重新生成manifest和deepcopy
make manifests generate

# 3. 安装CRD到集群
make install

# 4. 本地运行operator（不需要build Docker镜像）
make run

# 5. 另一个terminal，部署测试用的AgentDeployment
kubectl apply -f config/samples/basic-agent-deployment.yaml

# 6. 观察
kubectl get agentdeployments
kubectl describe agentdeployment customer-support-agent
```

### 构建Docker镜像

```bash
# 构建
make docker-build IMG=ghcr.io/ywc668/agentroll:latest

# 推送（需要先 docker login ghcr.io）
make docker-push IMG=ghcr.io/ywc668/agentroll:latest

# 部署到集群
make deploy IMG=ghcr.io/ywc668/agentroll:latest
```

## 四、下一步开发优先级

按这个顺序推进：

### Week 1-2: CRD + 基础Controller
- [x] 定义AgentDeployment CRD types
- [ ] Kubebuilder脚手架初始化
- [ ] 基础Reconcile循环：watch AgentDeployment → 创建Deployment + Service
- [ ] 单元测试

### Week 3-4: Argo Rollouts集成
- [ ] AgentDeployment → Argo Rollout resource转换
- [ ] AnalysisTemplate生成逻辑
- [ ] 端到端测试：创建AgentDeployment → 自动创建Rollout

### Week 5-6: 可观测性 + Demo
- [ ] Langfuse metric provider适配
- [ ] OTel sidecar注入
- [ ] 端到端demo录制

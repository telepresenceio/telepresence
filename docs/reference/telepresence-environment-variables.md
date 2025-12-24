# Telepresence Environment Variable Injection

This document explains how Telepresence captures environment variables from cluster pods and makes them available to your local development environment.

---

## Overview

When you create an intercept, Telepresence can automatically capture the environment variables from the intercepted pod's containers and inject them into your local process. This ensures your local development environment matches the cluster configuration.

---

## 1. Environment Variable Flow Architecture

This diagram shows the complete flow of environment variables from cluster pods to your local machine.

```mermaid
flowchart TB
    subgraph "Kubernetes Cluster"
        subgraph "Pod Configuration"
            Deployment["Deployment/StatefulSet: myapp"]
            PodSpec["Pod Spec: Environment Variables"]

            Deployment --> PodSpec
        end

        subgraph "Environment Variable Sources"
            DirectEnv["Direct env: DATABASE_URL, API_KEY"]
            ConfigMapRef["ConfigMap: app-config, shared-settings"]
            SecretRef["Secret: db-credentials, api-tokens"]
            FieldRef["Field References: metadata.name, status.podIP"]

            PodSpec --> DirectEnv
            PodSpec --> ConfigMapRef
            PodSpec --> SecretRef
            PodSpec --> FieldRef
        end

        subgraph "Running Pods (HPA: 3 replicas)"
            Pod1["Pod 1: myapp-abc123 with Traffic Agent"]
            Pod2["Pod 2: myapp-def456 with Traffic Agent"]
            Pod3["Pod 3: myapp-ghi789 with Traffic Agent"]

            Agent1[Traffic Agent 1]
            Agent2[Traffic Agent 2]
            Agent3[Traffic Agent 3]

            Pod1 --> Agent1
            Pod2 --> Agent2
            Pod3 --> Agent3
        end

        DirectEnv --> Pod1
        DirectEnv --> Pod2
        DirectEnv --> Pod3
        ConfigMapRef --> Pod1
        ConfigMapRef --> Pod2
        ConfigMapRef --> Pod3
        SecretRef --> Pod1
        SecretRef --> Pod2
        SecretRef --> Pod3
        FieldRef --> Pod1
        FieldRef --> Pod2
        FieldRef --> Pod3

        subgraph "Traffic Manager"
            TM["Traffic Manager: Orchestrates Intercepts"]
        end

        Agent1 <--> TM
        Agent2 <--> TM
        Agent3 <--> TM
    end

    subgraph "Developer Machine"
        subgraph "Telepresence Client"
            TPClient[Telepresence Daemon]
            EnvCollector[Environment Collector]
            EnvFile[".env file or export statements"]
        end

        subgraph "Local Process"
            LocalApp["Local Application (localhost:8080)"]
            AppEnv["Application reads environment variables"]
        end
    end

    subgraph "Intercept Creation Process"
        UserCmd["User runs: telepresence intercept myapp --port 8080 --env-file=.env"]
    end

    UserCmd --> TPClient
    TPClient --> TM
    TM --> Agent1

    Agent1 -->|Read container env| EnvCollector
    EnvCollector -->|Write to file| EnvFile
    EnvFile -->|Load into process| LocalApp
    LocalApp --> AppEnv

    Note1["Note: Only ONE pod's env is captured (typically first agent that responds)"]
    Note2["Note: All pods have identical env variables (from same Deployment)"]

    style DirectEnv fill:#99ff99
    style ConfigMapRef fill:#99ccff
    style SecretRef fill:#ff9999
    style FieldRef fill:#ffcc99
    style EnvFile fill:#ffff99
    style LocalApp fill:#ff99ff
    style Agent1 fill:#99ff99
    style Agent2 fill:#cccccc
    style Agent3 fill:#cccccc
```

**Key Points:**
- Environment variables come from multiple sources (direct, ConfigMaps, Secrets, field refs)
- **Only ONE pod's environment** is captured (typically the first agent that responds)
- Since all pods in the same Deployment have **identical environment variables**, this is sufficient
- Variables are written to a file (`.env` format or shell export format)
- Your local app loads these variables at startup

---

## 2. Environment Variable Capture Sequence

This sequence diagram shows the step-by-step process of capturing and injecting environment variables.

```mermaid
sequenceDiagram
    autonumber
    actor Dev as Developer
    participant CLI as Telepresence CLI
    participant Daemon as Telepresence Daemon
    participant TM as Traffic Manager
    participant Agent1 as Traffic Agent 1<br/>(Pod 1)
    participant Agent2 as Traffic Agent 2<br/>(Pod 2)
    participant Agent3 as Traffic Agent 3<br/>(Pod 3)
    participant K8S as Kubernetes API
    participant LocalApp as Local Application

    Dev->>CLI: telepresence intercept myapp<br/>--port 8080 --env-file=myapp.env

    CLI->>Daemon: CreateIntercept(request)
    Daemon->>TM: CreateIntercept RPC

    Note over TM: Intercept created<br/>Broadcasted to ALL agents

    par Broadcast to all agents
        TM->>Agent1: WatchIntercepts → interceptInfo
    and
        TM->>Agent2: WatchIntercepts → interceptInfo
    and
        TM->>Agent3: WatchIntercepts → interceptInfo
    end

    Note over TM,Agent3: All agents receive intercept

    TM->>Agent1: GetEnv(containerName)
    Agent1->>Agent1: Read /proc/1/environ<br/>or container env

    Agent1->>Agent1: Collect variables:<br/>- Direct env<br/>- From ConfigMaps<br/>- From Secrets<br/>- Field references

    Agent1-->>TM: EnvResponse{<br/>  DATABASE_URL=postgres://...<br/>  API_KEY=secret123<br/>  POD_NAME=myapp-abc123<br/>  KUBERNETES_SERVICE_HOST=...<br/>}

    TM-->>Daemon: EnvResponse

    Daemon->>Daemon: Format as .env file:<br/>DATABASE_URL="postgres://..."<br/>API_KEY="secret123"<br/>POD_NAME="myapp-abc123"

    Daemon->>Daemon: Write to myapp.env

    Daemon-->>CLI: Intercept created<br/>Env file written

    CLI-->>Dev: Intercept active<br/>Environment saved to myapp.env

    Dev->>Dev: Source environment:<br/>source myapp.env<br/>or export $(cat myapp.env)

    Dev->>LocalApp: Start local application<br/>with environment loaded

    LocalApp->>LocalApp: Read env variables:<br/>os.getenv("DATABASE_URL")<br/>os.getenv("API_KEY")

    Note over LocalApp: App runs with<br/>cluster environment

    rect rgb(240, 255, 240)
        Note over Agent1,LocalApp: Traffic from Pod 1 → Local App
        Agent1->>Daemon: Forward traffic
        Daemon->>LocalApp: Route to localhost:8080
        LocalApp-->>Daemon: Response
        Daemon-->>Agent1: Response
    end

    rect rgb(240, 240, 255)
        Note over Agent2,LocalApp: Traffic from Pod 2 → Same Local App
        Agent2->>Daemon: Forward traffic
        Daemon->>LocalApp: Route to localhost:8080 (same env)
        LocalApp-->>Daemon: Response
        Daemon-->>Agent2: Response
    end
```

**Process Steps:**
1. User requests intercept with `--env-file` flag
2. Traffic Manager creates intercept (broadcasted to all agents)
3. Traffic Manager requests environment variables from ONE agent (typically first responder)
4. Agent reads container's environment (all sources combined)
5. Agent returns environment variables to Traffic Manager
6. Traffic Manager forwards to Telepresence Daemon
7. Daemon writes variables to specified file (`.env` format)
8. Developer sources the file or loads it into local process
9. Local application runs with cluster environment variables
10. All intercepted traffic (from all pods) goes to the same local app with same environment

---

## 3. Environment Variable Sources Detail

This diagram breaks down the different sources of environment variables in Kubernetes pods.

```mermaid
flowchart LR
    subgraph "Pod Container Environment"
        FinalEnv[Container Environment<br/>Final merged variables]
    end

    subgraph "Source 1: Direct env"
        Direct1[env:<br/>- name: DATABASE_URL<br/>  value: postgres://db:5432]
        Direct2[env:<br/>- name: LOG_LEVEL<br/>  value: info]
    end

    subgraph "Source 2: ConfigMap"
        CM[ConfigMap: app-config]
        CMData[data:<br/>  APP_NAME: myapp<br/>  MAX_CONNECTIONS: 100<br/>  FEATURE_FLAG_X: true]

        CM --> CMData

        EnvFrom1[envFrom:<br/>- configMapRef:<br/>    name: app-config]
        EnvRef1[env:<br/>- name: SPECIFIC_VALUE<br/>  valueFrom:<br/>    configMapKeyRef:<br/>      name: app-config<br/>      key: APP_NAME]
    end

    subgraph "Source 3: Secret"
        Sec[Secret: db-credentials]
        SecData[data:<br/>  DB_PASSWORD: cGFzc3dvcmQxMjM=<br/>  API_TOKEN: dG9rZW4xMjM0NTY=]

        Sec --> SecData

        EnvFrom2[envFrom:<br/>- secretRef:<br/>    name: db-credentials]
        EnvRef2[env:<br/>- name: DB_PASS<br/>  valueFrom:<br/>    secretKeyRef:<br/>      name: db-credentials<br/>      key: DB_PASSWORD]
    end

    subgraph "Source 4: Field References"
        Field1[env:<br/>- name: POD_NAME<br/>  valueFrom:<br/>    fieldRef:<br/>      fieldPath: metadata.name]
        Field2[env:<br/>- name: POD_IP<br/>  valueFrom:<br/>    fieldRef:<br/>      fieldPath: status.podIP]
        Field3[env:<br/>- name: NODE_NAME<br/>  valueFrom:<br/>    fieldRef:<br/>      fieldPath: spec.nodeName]
    end

    subgraph "Source 5: Resource Fields"
        Resource1[env:<br/>- name: MEMORY_LIMIT<br/>  valueFrom:<br/>    resourceFieldRef:<br/>      resource: limits.memory]
        Resource2[env:<br/>- name: CPU_REQUEST<br/>  valueFrom:<br/>    resourceFieldRef:<br/>      resource: requests.cpu]
    end

    Direct1 --> FinalEnv
    Direct2 --> FinalEnv
    EnvFrom1 --> FinalEnv
    EnvRef1 --> FinalEnv
    EnvFrom2 --> FinalEnv
    EnvRef2 --> FinalEnv
    Field1 --> FinalEnv
    Field2 --> FinalEnv
    Field3 --> FinalEnv
    Resource1 --> FinalEnv
    Resource2 --> FinalEnv

    FinalEnv --> TrafficAgent[Traffic Agent<br/>Captures All]
    TrafficAgent --> TPDaemon[Telepresence Daemon]
    TPDaemon --> EnvFile[.env file]

    style FinalEnv fill:#ffff99
    style CMData fill:#99ccff
    style SecData fill:#ff9999
    style EnvFile fill:#99ff99
```

**Captured Variables Include:**
- ✅ Direct `env` values
- ✅ All keys from `envFrom.configMapRef`
- ✅ All keys from `envFrom.secretRef`
- ✅ Specific keys from `configMapKeyRef`
- ✅ Specific keys from `secretKeyRef`
- ✅ Field references (pod name, IP, node, namespace)
- ✅ Resource field references (limits, requests)
- ✅ Kubernetes-injected variables (SERVICE_HOST, SERVICE_PORT)

---

## 4. Usage Examples

### Example 1: Basic Environment Capture

```bash
# Create intercept and save environment to file
telepresence intercept myapp --port 8080 --env-file=myapp.env

# File contents (myapp.env):
# DATABASE_URL="postgres://postgres:5432/mydb"
# REDIS_URL="redis://redis:6379"
# API_KEY="secret123"
# LOG_LEVEL="info"
# POD_NAME="myapp-abc123"
# POD_IP="10.244.1.5"

# Load environment and run local app
source myapp.env
python app.py

# Or use with docker-compose
docker-compose --env-file myapp.env up
```

### Example 2: JSON Format

```bash
# Get environment in JSON format
telepresence intercept myapp --port 8080 --env-json=myapp.json

# File contents (myapp.json):
# {
#   "DATABASE_URL": "postgres://postgres:5432/mydb",
#   "REDIS_URL": "redis://redis:6379",
#   "API_KEY": "secret123",
#   "LOG_LEVEL": "info",
#   "POD_NAME": "myapp-abc123",
#   "POD_IP": "10.244.1.5"
# }

# Use with tools that accept JSON config
```

### Example 3: Docker Integration

```bash
# Capture environment
telepresence intercept myapp --port 8080 --env-file=cluster.env

# Run local container with cluster environment
docker run --env-file cluster.env -p 8080:8080 myapp:latest
```

### Example 4: Programming Language Integration

**Python:**
```python
# Load .env file using python-dotenv
from dotenv import load_dotenv
import os

load_dotenv('myapp.env')

database_url = os.getenv('DATABASE_URL')
api_key = os.getenv('API_KEY')
```

**Node.js:**
```javascript
// Load .env file using dotenv
require('dotenv').config({ path: 'myapp.env' });

const databaseUrl = process.env.DATABASE_URL;
const apiKey = process.env.API_KEY;
```

**Go:**
```go
// Load .env file using godotenv
import "github.com/joho/godotenv"

godotenv.Load("myapp.env")

databaseUrl := os.Getenv("DATABASE_URL")
apiKey := os.Getenv("API_KEY")
```

---

## 5. Important Considerations

### Which Pod's Environment is Captured?

```mermaid
flowchart LR
    TM[Traffic Manager] -->|GetEnv request| Agent1[Agent 1<br/>myapp-abc123]
    TM -.->|Not queried| Agent2[Agent 2<br/>myapp-def456]
    TM -.->|Not queried| Agent3[Agent 3<br/>myapp-ghi789]

    Agent1 -->|Env response| TM
    TM --> EnvFile[.env file]

    Note1[All pods have<br/>IDENTICAL env<br/>from same Deployment]

    style Agent1 fill:#99ff99
    style Agent2 fill:#cccccc
    style Agent3 fill:#cccccc
    style Note1 fill:#ffff99
```

**Key Points:**
- Only **one agent** is queried for environment variables (typically the first to respond)
- This is **sufficient** because all pods in the same Deployment/StatefulSet have identical environment variables
- Pod-specific variables (like `POD_NAME`, `POD_IP`) will be from that one pod
- If you need different pod's environment, you can specify with `--agent-name` flag

### Environment Variable Precedence

When you run your local app:

1. **Local system environment** (existing variables on your machine)
2. **Loaded .env file** (from `--env-file`)
3. **Explicit exports** (if you manually set variables)

Use `export $(cat myapp.env | xargs)` to override local variables with cluster values.

### Security Considerations

⚠️ **Important Security Notes:**

1. **Secrets in .env files**: The `.env` file contains decoded secrets (not base64)
2. **File permissions**: Ensure `.env` files have restricted permissions (`chmod 600`)
3. **Git ignore**: Add `.env` to `.gitignore` to avoid committing secrets
4. **Cleanup**: Delete `.env` files after development sessions

```bash
# Set proper permissions
chmod 600 myapp.env

# Add to .gitignore
echo "*.env" >> .gitignore

# Cleanup after session
telepresence leave
rm myapp.env
```

---

## 6. Comparison with Other Approaches

### Without Telepresence Environment Injection

```mermaid
flowchart TB
    Dev[Developer] -->|Manual steps| Step1["Step 1: kubectl get configmap"]
    Step1 --> Step2["Step 2: kubectl get secret"]
    Step2 --> Step3["Step 3: Copy values manually"]
    Step3 --> Step4["Step 4: Create local .env"]
    Step4 --> Step5["Step 5: Keep in sync"]
    Step5 -.->|Changes in cluster| Step1

    style Step5 fill:#ff9999
```

**Problems:**
- ❌ Manual, error-prone process
- ❌ Secrets need manual base64 decoding
- ❌ No automatic sync with cluster changes
- ❌ Field references (POD_NAME, etc.) hard to replicate

### With Telepresence Environment Injection

```mermaid
flowchart LR
    Dev[Developer] -->|One command| Cmd["telepresence intercept --env-file=.env"]
    Cmd --> Auto["Automatic capture: All sources, Decoded secrets, Field refs included"]
    Auto --> Ready[Ready to use]

    style Ready fill:#99ff99
```

**Benefits:**
- ✅ Single command
- ✅ Automatic collection from all sources
- ✅ Secrets automatically decoded
- ✅ Field references automatically resolved
- ✅ Always in sync with cluster

---

## Summary

**Environment Variable Injection provides:**

1. **Automatic Capture**: All environment variables from all sources
2. **Multiple Formats**: `.env` files, JSON, or direct export
3. **Decoded Secrets**: No manual base64 decoding needed
4. **Field References**: Pod name, IP, node name automatically included
5. **Development Parity**: Your local app runs with exact cluster configuration
6. **Simple Integration**: Works with Docker, docker-compose, and all programming languages

**Typical Workflow:**

```bash
# 1. Create intercept with environment capture
telepresence intercept myapp --port 8080 --env-file=myapp.env

# 2. Load environment
source myapp.env

# 3. Run local app (now has cluster config)
npm start
# or
python app.py
# or
docker-compose up

# 4. Develop and test with production-like config

# 5. Clean up
telepresence leave
rm myapp.env
```
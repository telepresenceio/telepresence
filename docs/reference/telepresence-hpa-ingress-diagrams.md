# Telepresence HPA + Ingress Architecture Diagrams

This document provides comprehensive visualizations of how Telepresence handles Horizontal Pod Autoscaler (HPA) scenarios with Ingress traffic.

## ⚠️ IMPORTANT: Multi-Pod Intercept Behavior

**This document has been updated to reflect the correct behavior discovered through code analysis.**

**Key Finding**: When you create a basic intercept on an HPA-managed workload with multiple replicas, **ALL pods intercept and forward traffic to your local machine**, not just one pod.

- See `telepresence-hpa-traffic-routing-discovery.md` for complete code-level analysis
- See `telepresence-architecture-diagrams.md` for updated sequence diagrams

---

## 1. Overall Architecture Diagram - CORRECTED

This diagram shows the complete flow from external clients through Ingress to multiple pod replicas. **ALL pods intercept traffic and forward to the same developer machine.**

```mermaid
graph TB
    subgraph "External"
        Client[External Client]
    end

    subgraph "Kubernetes Cluster"
        subgraph "Ingress Layer"
            Ingress[Ingress Controller<br/>UNCHANGED]
        end

        subgraph "Service Layer"
            Service[Kubernetes Service<br/>UNCHANGED<br/>ClusterIP: 10.0.0.100<br/>Load Balancer]
        end

        subgraph "Pod Replicas (HPA Managed)"
            subgraph "Pod 1 - INTERCEPTED"
                Agent1[Traffic Agent 1<br/>Sidecar<br/>ClientSession: xyz]
                App1[App Container<br/>Port 8080]
                Agent1 -.Forwards ALL traffic.-> Forward1[To Developer]
            end

            subgraph "Pod 2 - INTERCEPTED"
                Agent2[Traffic Agent 2<br/>Sidecar<br/>ClientSession: xyz]
                App2[App Container<br/>Port 8080]
                Agent2 -.Forwards ALL traffic.-> Forward2[To Developer]
            end

            subgraph "Pod 3 - INTERCEPTED"
                Agent3[Traffic Agent 3<br/>Sidecar<br/>ClientSession: xyz]
                App3[App Container<br/>Port 8080]
                Agent3 -.Forwards ALL traffic.-> Forward3[To Developer]
            end
        end

        subgraph "Traffic Manager"
            TM[Traffic Manager Pod<br/>BROADCASTS intercept to<br/>ALL matching agents<br/>Filter: Spec.Agent == agent.Name]
        end
    end

    subgraph "Developer Machine"
        DevMachine[Local Process<br/>localhost:9090<br/>Receives 100% traffic]
        TPClient[Telepresence Client<br/>ClientSession: xyz]
    end

    Client -->|HTTP/HTTPS Request| Ingress
    Ingress -->|Routes to Service| Service
    Service -->|~33% Round Robin| Agent1
    Service -->|~33% Round Robin| Agent2
    Service -->|~33% Round Robin| Agent3

    Agent1 -->|gRPC Tunnel<br/>100% of Pod 1 traffic| TM
    Agent2 -->|gRPC Tunnel<br/>100% of Pod 2 traffic| TM
    Agent3 -->|gRPC Tunnel<br/>100% of Pod 3 traffic| TM

    TM -->|Routes ALL traffic<br/>to same ClientSession| TPClient
    TPClient -->|Forwards 100%<br/>of all traffic| DevMachine

    Forward1 -.-> TPClient
    Forward2 -.-> TPClient
    Forward3 -.-> TPClient

    style Agent1 fill:#ff9999
    style Agent2 fill:#ff9999
    style Agent3 fill:#ff9999
    style App1 fill:#ffcccc
    style App2 fill:#ffcccc
    style App3 fill:#ffcccc
    style Service fill:#99ff99
    style Ingress fill:#99ff99
    style TM fill:#ffcc99
    style DevMachine fill:#ff99ff
    style TPClient fill:#ffccff
```

**Key Points (CORRECTED):**
- Service and Ingress are **NOT modified** by Telepresence
- All pods have Traffic Agent sidecars injected via mutating webhook
- **ALL pods have active intercepts** (all red) - not just one!
- All agents share the **same ClientSession ID** (xyz)
- Traffic Manager **broadcasts** the intercept to ALL matching agents
- Service load-balances ~33% to each pod
- Each pod forwards 100% of its received traffic to developer
- **Result: Developer receives 100% of total traffic (33% + 33% + 33%)**

---

## 2. Traffic Flow Decision Diagram - CORRECTED

This flowchart shows the decision logic when a request arrives at a pod with a Traffic Agent. **All pods in the workload have active intercepts.**

```mermaid
flowchart TD
    Start[Request Arrives at Pod] --> HasAgent{Does Pod Have<br/>Traffic Agent?}

    HasAgent -->|No| DirectApp[Forward Directly<br/>to App Container]
    HasAgent -->|Yes| CheckIntercept{Is There an<br/>Active Intercept<br/>for this Workload?}

    CheckIntercept -->|No| ForwardApp[Forward to<br/>App Container]
    CheckIntercept -->|Yes| CheckHTTP{Is HTTP Intercept<br/>with Filters?}

    CheckHTTP -->|No<br/>TCP/Basic Intercept| InterceptDev[Intercept ALL Traffic<br/>Forward to Developer<br/>ClientSession xyz]
    CheckHTTP -->|Yes<br/>HTTP with Filters| MatchHeaders{Do Headers/Path<br/>Match Filters?}

    MatchHeaders -->|Yes| InterceptDev2[Intercept Matching Traffic<br/>Forward to Developer<br/>ClientSession xyz]
    MatchHeaders -->|No| ForwardApp3[Forward to<br/>App Container]

    CheckHTTP -->|Wiretap Mode| WiretapBoth[Copy Traffic to Developer<br/>AND Forward to App]

    DirectApp --> End[Response Returned]
    ForwardApp --> End
    ForwardApp3 --> End
    InterceptDev --> DevProcess[Developer's<br/>Local Process<br/>localhost:9090]
    InterceptDev2 --> DevProcess
    WiretapBoth --> DevProcess
    WiretapBoth --> AppContainer[App Container]
    DevProcess --> End
    AppContainer --> End

    style InterceptDev fill:#ff9999
    style InterceptDev2 fill:#ff9999
    style DevProcess fill:#ff99ff
    style ForwardApp fill:#99ccff
    style ForwardApp3 fill:#99ccff
    style WiretapBoth fill:#ffcc99
    style CheckIntercept fill:#ffffcc
```

**Key Decision Points (CORRECTED):**
1. **Agent Check**: Only pods with agents can intercept
2. **Active Intercept for Workload**: Checks if there's an active intercept for the entire workload (not individual pod)
3. **Intercept Type**: TCP/Basic intercepts forward ALL traffic, HTTP intercepts can filter
4. **HTTP Filters**: Optional header/path matching for selective interception (applies to all pods)
5. **Wiretap Mode**: Special mode that copies traffic without disrupting normal flow
6. **All Pods Process Same Logic**: Every pod with an agent makes the same decisions (no "is this the intercepted pod" check)

---

## 3a. HPA Scale UP Scenario (3→4 Pods) - CORRECTED

This sequence diagram shows what happens when HPA scales up the deployment. **ALL pods receive the intercept via broadcast.**

```mermaid
sequenceDiagram
    participant HPA as HPA Controller
    participant K8s as Kubernetes API
    participant Webhook as Mutating Webhook
    participant TM as Traffic Manager
    participant Pod1 as Pod 1<br/>(INTERCEPTED)
    participant Pod2 as Pod 2<br/>(INTERCEPTED)
    participant Pod3 as Pod 3<br/>(INTERCEPTED)
    participant Pod4 as Pod 4<br/>(NEW)
    participant Dev as Developer<br/>ClientSession xyz

    Note over HPA,Dev: Initial State: 3 pods, ALL intercepted

    HPA->>K8s: Scale deployment to 4 replicas
    K8s->>Webhook: Pod creation event
    Webhook->>Webhook: Check namespace/labels
    Webhook->>K8s: Inject Traffic Agent sidecar
    K8s->>Pod4: Create pod with agent

    Pod4->>Pod4: Traffic Agent starts
    Pod4->>TM: Register AgentSession<br/>(Name: myapp, pod UID: xyz-new)
    TM->>TM: Add to agent map
    TM->>TM: Filter: agent.Name == "myapp" ✓

    Note over TM: BROADCAST: Existing intercept sent to new pod

    TM->>Pod4: WatchIntercepts → interceptInfo<br/>ClientSession: xyz
    Pod4->>Pod4: Setup port handlers
    Pod4->>TM: ReviewIntercept
    TM->>Dev: Establish tunnel to Pod 4

    Note over Pod1,Pod4: Service now load balances 25% to each pod

    rect rgb(255, 240, 240)
        Note over Pod1,Dev: Pod 1: 25% of traffic → 100% to Dev
        Pod1->>Dev: ALL traffic forwarded (25% of total)
    end
    rect rgb(240, 255, 240)
        Note over Pod2,Dev: Pod 2: 25% of traffic → 100% to Dev
        Pod2->>Dev: ALL traffic forwarded (25% of total)
    end
    rect rgb(240, 240, 255)
        Note over Pod3,Dev: Pod 3: 25% of traffic → 100% to Dev
        Pod3->>Dev: ALL traffic forwarded (25% of total)
    end
    rect rgb(255, 255, 240)
        Note over Pod4,Dev: Pod 4: 25% of traffic → 100% to Dev
        Pod4->>Dev: ALL traffic forwarded (25% of total)
    end

    Note over HPA,Dev: Result: 4 pods, ALL intercepted, Dev gets 100% traffic
```

**Key Behaviors (CORRECTED):**
- New pod automatically gets Traffic Agent via mutating webhook
- Agent registers with Traffic Manager using **workload name** ("myapp")
- Traffic Manager's filter matches: `agent.Name == "myapp"` ✓
- **Existing intercept is BROADCASTED to new pod** (same as other pods)
- New pod receives **same ClientSession ID** as other pods
- Service load-balances 25% to each pod (was 33%, now 25%)
- **Developer still receives 100% of total traffic** (25% + 25% + 25% + 25%)
- No disruption to active intercept

---

## 3b. HPA Scale DOWN Scenario (3→2 Pods) - CORRECTED

This sequence diagram shows what happens when HPA scales down. **ALL remaining pods continue to intercept.**

```mermaid
sequenceDiagram
    participant HPA as HPA Controller
    participant K8s as Kubernetes API
    participant TM as Traffic Manager
    participant Pod1 as Pod 1<br/>(INTERCEPTED)
    participant Pod2 as Pod 2<br/>(INTERCEPTED)
    participant Pod3 as Pod 3<br/>(INTERCEPTED, to be terminated)
    participant Dev as Developer<br/>ClientSession xyz

    Note over HPA,Dev: Initial State: 3 pods, ALL intercepted

    HPA->>K8s: Scale deployment to 2 replicas
    K8s->>Pod3: Terminate pod
    Pod3->>TM: AgentSession disconnected
    TM->>TM: Remove from agent map
    TM->>Dev: Close tunnel to Pod 3

    Note over Pod1,Pod2: Service now load balances across 2 pods

    rect rgb(255, 240, 240)
        Note over Pod1,Dev: Pod 1: 50% of traffic → 100% to Dev
        Pod1->>Dev: ALL traffic forwarded (50% of total)
    end
    rect rgb(240, 255, 240)
        Note over Pod2,Dev: Pod 2: 50% of traffic → 100% to Dev
        Pod2->>Dev: ALL traffic forwarded (50% of total)
    end

    Note over HPA,Dev: Result: 2 pods remain, BOTH still intercepted<br/>Dev still gets 100% traffic (50% + 50%)

    rect rgb(255, 220, 220)
        Note over HPA,Dev: SCENARIO 2: Scale down to 1 pod

        HPA->>K8s: Scale down again (2→1)
        K8s->>Pod2: Terminate Pod 2
        Pod2->>TM: AgentSession disconnected
        TM->>TM: Remove from agent map
        TM->>Dev: Close tunnel to Pod 2

        Note over Pod1,Dev: Only 1 pod remains

        rect rgb(255, 240, 240)
            Note over Pod1,Dev: Pod 1: 100% of traffic → 100% to Dev
            Pod1->>Dev: ALL traffic forwarded (100% of total)
        end

        Note over HPA,Dev: Result: 1 pod remains, STILL intercepted<br/>Dev still gets 100% traffic
    end
```

**Key Behaviors (CORRECTED):**
- When pod is terminated, its tunnel is closed
- **ALL remaining pods STILL have active intercepts** (not just one)
- Intercept does **NOT enter WAITING state** (other pods still active)
- Traffic Manager simply removes terminated pod from agent map
- Remaining pods continue forwarding **100% of their traffic** to developer
- **Developer still receives 100% of total production traffic**
- Service load-balances across fewer pods (50% each with 2 pods, 100% with 1 pod)
- No interruption to intercept - seamless continuation with remaining pods
- The intercept only ends when **ALL pods are gone** or user runs `telepresence leave`

---

## 4. Agent Injection & Session Management

This diagram shows how agents are injected and how sessions are managed.

```mermaid
flowchart TB
    subgraph "Pod Creation Flow"
        PodCreate[Pod Creation Request] --> Webhook{Mutating Webhook<br/>Filter Check}
        Webhook -->|Namespace match +<br/>No opt-out label| Inject[Inject Traffic Agent<br/>Sidecar Container]
        Webhook -->|No match or<br/>opt-out| NoInject[Create Pod Without Agent]
        Inject --> PodStart[Pod Starts]
        NoInject --> PodStart
    end

    subgraph "Agent Registration"
        PodStart --> AgentStart{Agent Container<br/>Started?}
        AgentStart -->|Yes| Connect[Connect to<br/>Traffic Manager]
        AgentStart -->|No| AppOnly[Only App Container Runs]
        Connect --> Register[Register AgentSession<br/>with Pod UID]
        Register --> SessionCreated[Session Created in TM]
    end

    subgraph "Traffic Manager State"
        SessionCreated --> MapUpdate[Add to Agent Map<br/>podUID → AgentSession]
        MapUpdate --> ServiceWatch[Watch K8s Services]
        ServiceWatch --> EndpointTrack[Track Pod Endpoints<br/>per Service]
        EndpointTrack --> ConfigGen[Generate Agent Config<br/>with Service Info]
        ConfigGen --> SendConfig[Send Config to All Agents]
    end

    subgraph "Session Lifecycle"
        SendConfig --> Active[Agent Session ACTIVE]
        Active --> Monitor[Monitor Connection]
        Monitor -->|Connection Lost| Remove[Remove from Agent Map]
        Monitor -->|Config Mismatch| Evict[Evict Pod<br/>Force Restart]
        Remove --> SessionEnd[Session Ended]
        Evict --> SessionEnd
    end

    style Inject fill:#99ff99
    style Register fill:#99ccff
    style SessionCreated fill:#ffcc99
    style MapUpdate fill:#ffcc99
    style Active fill:#99ff99
    style Remove fill:#ff9999
    style Evict fill:#ff9999
```

**Key Concepts:**
- **Mutating Webhook**: Intercepts pod creation, injects agent sidecar
- **Pod UID**: Unique identifier used to track each agent session
- **Agent Map**: Traffic Manager maintains map of all active agent sessions (keyed by workload name)
- **Service Watching**: TM tracks which pods are endpoints for which services
- **Config Sync**: All agents receive updated configs when services change
- **Eviction**: Pods with mismatched configs are evicted and restarted
- **Intercept Broadcast**: When an agent registers, if there's an existing intercept for its workload name, it immediately receives it via `WatchIntercepts` (see Diagram #5)

---

## 5. Intercept Lifecycle with Multiple Pods - CORRECTED

This state diagram shows how an intercept transitions between states. **ALL pods receive the intercept via broadcast mechanism.**

```mermaid
stateDiagram-v2
    [*] --> NO_INTERCEPT: No intercept created

    NO_INTERCEPT --> NO_AGENT: User runs telepresence intercept
    NO_AGENT --> WAITING: Agent becomes available
    NO_INTERCEPT --> ACTIVE: Agent already available (ALL receive intercept)

    WAITING --> ACTIVE: Agent(s) available (ALL receive intercept)

    ACTIVE --> ACTIVE: HPA scales UP (new pod receives broadcast)

    ACTIVE --> ACTIVE: HPA scales DOWN (remaining pods continue)

    ACTIVE --> NO_INTERCEPT: User runs telepresence leave
    WAITING --> NO_INTERCEPT: User runs telepresence leave

    NO_AGENT --> NO_INTERCEPT: User cancels telepresence leave

    state ACTIVE {
        [*] --> AllPodsIntercepting: ALL pods forward traffic

        state AllPodsIntercepting {
            [*] --> TCPMode: TCP or full HTTP intercept
            [*] --> HTTPFilterMode: HTTP intercept with headers/path
            HTTPFilterMode --> TCPMode: Filter removed
            TCPMode --> HTTPFilterMode: Filter added
        }

        AllPodsIntercepting --> PodScaling: HPA event
        PodScaling --> AllPodsIntercepting: Still intercepting

        note right of AllPodsIntercepting
            ALL pods in workload:
            - Receive same intercept
            - Forward to same ClientSession
            - Apply same filters
        end note
    }

    note right of NO_AGENT
        No agents available
        (no pods or no sidecars)
    end note

    note right of WAITING
        Intercept created
        Waiting for agent(s)
        to become available
    end note

    note right of ACTIVE
        ALL pods with matching
        workload name receive
        the intercept (broadcast)
    end note

    style NO_AGENT fill:#ffcccc
    style WAITING fill:#ffffcc
    style ACTIVE fill:#ccffcc
    style NO_INTERCEPT fill:#cdcdcd
    style AllPodsIntercepting fill:#99ff99
```

**State Descriptions (CORRECTED):**

1. **NO_INTERCEPT**: Default state, no intercept created
2. **NO_AGENT**: Intercept requested but no agents available (no pods with sidecars)
3. **WAITING**: Intercept created, waiting for agent(s) to become available
4. **ACTIVE**: One or more agents available, **ALL matching agents receive and handle the intercept**
   - The intercept object exists in Traffic Manager
   - The `WatchIntercepts` filter broadcasts it to ALL agents where `agent.Name == Spec.Agent`
   - ALL agents forward their traffic to the same `ClientSession.SessionId`
   - HPA scale up/down does NOT change state (remains ACTIVE with different number of pods)
   - Sub-states for full vs filtered HTTP interception

**Transitions (CORRECTED):**
- HPA scale UP: Stays in ACTIVE (new pod receives broadcast intercept)
- HPA scale DOWN: Stays in ACTIVE (remaining pods continue intercepting)
- User runs `telepresence leave`: ACTIVE → NO_INTERCEPT (all pods stop intercepting)

---

## 6. HTTP Intercept Filtering

This flowchart shows how HTTP header/path filtering works for selective interception.

```mermaid
flowchart TD
    Start[HTTP Request Arrives<br/>at Intercepted Pod] --> ParseReq[Traffic Agent Parses<br/>HTTP Request]

    ParseReq --> CheckFilters{Are Intercept<br/>Filters Configured?}

    CheckFilters -->|No Filters| InterceptAll[Intercept ALL Requests<br/>Forward to Developer]

    CheckFilters -->|Yes| CheckHeaders{Check Header<br/>Filters}

    CheckHeaders -->|Match| CheckPath{Check Path<br/>Filters}
    CheckHeaders -->|No Match| ForwardApp[Forward to<br/>App Container]

    CheckPath -->|Match| InterceptFiltered[Intercept Matching Request<br/>Forward to Developer]
    CheckPath -->|No Match| ForwardApp2[Forward to<br/>App Container]

    subgraph "Example Filters"
        direction TB
        Ex1[Header: x-intercept=dev]
        Ex2[Path: /api/v2/*]
        Ex3[Header: user-agent=test-client]
        Ex4[Combination: Header AND Path]
    end

    InterceptAll --> DevProcess[Developer's Local Process<br/>localhost:9090]
    InterceptFiltered --> DevProcess

    DevProcess --> DevResponse[Developer Returns Response]
    DevResponse --> ClientResp[Response to Client]

    ForwardApp --> AppContainer[App Container<br/>Port 8080]
    ForwardApp2 --> AppContainer
    AppContainer --> AppResponse[App Returns Response]
    AppResponse --> ClientResp

    style InterceptAll fill:#ff9999
    style InterceptFiltered fill:#ff9999
    style DevProcess fill:#ff99ff
    style ForwardApp fill:#99ccff
    style ForwardApp2 fill:#99ccff
    style AppContainer fill:#99ccff
```

**Filter Types:**
- **Header Filters**: Match specific HTTP headers (e.g., `x-intercept: dev`)
- **Path Filters**: Match URL paths with wildcards (e.g., `/api/v2/*`)
- **Combined Filters**: Both header AND path must match

**Use Cases:**
1. **Dev Environment Testing**: Only intercept requests with specific headers
2. **API Version Testing**: Only intercept specific API versions
3. **User Simulation**: Only intercept traffic from test users
4. **Partial Migration**: Test new code with subset of production traffic

**Benefits:**
- Same pod handles both intercepted and normal traffic
- No need to modify load balancer or routing rules
- Allows A/B testing and gradual rollouts
- Production traffic continues unaffected

---

## 7. Service + Endpoint Tracking

This diagram shows how Traffic Manager watches services and manages agent configurations.

```mermaid
flowchart TB
    subgraph "Traffic Manager Watchers"
        TMStart[Traffic Manager Starts] --> InitWatch[Initialize K8s Watchers]
        InitWatch --> ServiceWatch[Watch Services<br/>in Namespace]
        InitWatch --> PodWatch[Watch Pods<br/>in Namespace]
        InitWatch --> EndpointWatch[Watch Endpoints<br/>in Namespace]
    end

    subgraph "Service Event Processing"
        ServiceWatch --> SvcEvent{Service Event}
        SvcEvent -->|Created| AddService[Add Service to Map]
        SvcEvent -->|Modified| UpdateService[Update Service Info]
        SvcEvent -->|Deleted| RemoveService[Remove Service from Map]

        AddService --> GetEndpoints[Query Service Endpoints]
        UpdateService --> GetEndpoints
        GetEndpoints --> MapPods[Map Endpoints to Pod UIDs]
    end

    subgraph "Agent Session Tracking"
        MapPods --> CheckAgents{Do Pods Have<br/>Agent Sessions?}
        CheckAgents -->|Yes| LinkAgents[Link Service → Agents]
        CheckAgents -->|No| MarkMissing[Mark as Pods Without Agents]

        LinkAgents --> AgentMap[Update Agent Map<br/>podUID → Services]
    end

    subgraph "Config Generation & Distribution"
        AgentMap --> GenerateConfig[Generate Agent Config<br/>for Each Pod]
        GenerateConfig --> ConfigContent[Config Contains:<br/>- Service ClusterIP<br/>- Service Ports<br/>- DNS Names<br/>- Other Pods in Service]

        ConfigContent --> SendToAgents[Send Config to All Agents]
        SendToAgents --> AgentReceive[Agents Receive Config]

        AgentReceive --> CompareConfig{Config Match?}
        CompareConfig -->|Match| AgentOK[Agent Continues]
        CompareConfig -->|Mismatch| EvictPod[Evict Pod<br/>Trigger Restart]
    end

    subgraph "Agent Connection Events"
        NewAgent[New Agent Connects] --> RegisterSession[Register AgentSession<br/>with Pod UID]
        RegisterSession --> QueryServices[Query K8s for Services<br/>with This Pod as Endpoint]
        QueryServices --> SendInitialConfig[Send Initial Config to Agent]
        SendInitialConfig --> AgentReceive

        AgentDisconnect[Agent Disconnects] --> RemoveSession[Remove AgentSession]
        RemoveSession --> UpdateOthers[Update Configs for<br/>Remaining Agents]
        UpdateOthers --> SendToAgents
    end

    style ServiceWatch fill:#99ff99
    style AgentMap fill:#ffcc99
    style GenerateConfig fill:#99ccff
    style EvictPod fill:#ff9999
    style AgentOK fill:#99ff99
```

**Key Processes:**

1. **Service Watching**:
   - Traffic Manager watches all Services in the namespace
   - Tracks which pods are endpoints for each service
   - Updates when services are created, modified, or deleted

2. **Agent Session Tracking**:
   - Each agent registers with unique pod UID
   - TM maintains bidirectional map: Service ↔ Agents
   - Links services to the agents running in their endpoint pods

3. **Config Generation**:
   - Each agent receives config with:
     - All services it's an endpoint for
     - ClusterIPs and ports
     - DNS names
     - List of other pods in the same service
   - Config regenerated when services or endpoints change

4. **Config Validation**:
   - Agents compare received config with their current state
   - **Mismatch Detection**: If pod's actual services don't match config
   - **Eviction**: Pod is evicted and restarted to resync
   - Prevents stale state and ensures consistency

5. **Dynamic Updates**:
   - New agent connects → Get current service state
   - Service changes → All affected agents get new config
   - Agent disconnects → Other agents notified of endpoint change

---

## 8. Traffic Distribution Visualization - CORRECTED

This diagram shows how traffic is distributed across pods with a basic intercept active. **ALL pods intercept and forward to the developer.**

```mermaid
graph TB
    subgraph "Ingress Traffic"
        Traffic[100% Traffic<br/>from Ingress]
    end

    subgraph "Kubernetes Service Load Balancer"
        Service[Service: my-app<br/>Load Balances Round-Robin]
    end

    subgraph "Pod Distribution (3 Replicas)"
        Pod1[Pod 1<br/>INTERCEPTED<br/>⚠️]
        Pod2[Pod 2<br/>INTERCEPTED<br/>⚠️]
        Pod3[Pod 3<br/>INTERCEPTED<br/>⚠️]
    end

    subgraph "Traffic Destinations"
        Developer[Developer Machine<br/>localhost:9090<br/>100% of total traffic<br/>33% + 33% + 33%]
    end

    Traffic -->|100%| Service
    Service -.33%.-> Pod1
    Service -.33%.-> Pod2
    Service -.33%.-> Pod3

    Pod1 -->|Intercept Active<br/>ALL traffic redirected| Developer
    Pod2 -->|Intercept Active<br/>ALL traffic redirected| Developer
    Pod3 -->|Intercept Active<br/>ALL traffic redirected| Developer

    style Traffic fill:#e0e0e0
    style Service fill:#99ff99
    style Pod1 fill:#ff9999
    style Pod2 fill:#ff9999
    style Pod3 fill:#ff9999
    style Developer fill:#ff99ff

    Note1[Note: Service load balancing<br/>is unchanged by Telepresence]
    Note2[Note: ALL pods forward to<br/>SAME ClientSession]
    Note3[Note: Developer receives<br/>100% of production traffic]
```

### Traffic Distribution Table - CORRECTED

**For TCP/Basic Intercepts (no HTTP filtering):**

| Scenario | Total Pods | Intercepted Pods | Traffic to Developer | Traffic to Cluster |
|----------|-----------|------------------|---------------------|-------------------|
| 1 pod    | 1         | 1 (100%)         | 100%                | 0%                |
| 2 pods   | 2         | 2 (100%)         | 100%                | 0%                |
| 3 pods   | 3         | 3 (100%)         | 100%                | 0%                |
| 4 pods   | 4         | 4 (100%)         | 100%                | 0%                |
| 5 pods   | 5         | 5 (100%)         | 100%                | 0%                |

**Key Points (CORRECTED):**
- Service load balancing is **unmodified** by Telepresence
- **ALL pods** in the workload receive the same intercept (broadcast mechanism)
- **ALL pods** forward 100% of their received traffic to developer
- Developer receives **100% of total production traffic**
- The number of replicas **does NOT affect** the percentage of traffic reaching developer
- **HTTP filtering** is the ONLY way to reduce traffic to developer

### With HTTP Filtering - CORRECTED

```mermaid
graph TB
    subgraph "Traffic with HTTP Filters"
        TotalTraffic[100% Traffic]
    end

    subgraph "ALL Pods Inspect Traffic"
        Agent1[Pod 1 Traffic Agent<br/>Inspects Headers/Path]
        Agent2[Pod 2 Traffic Agent<br/>Inspects Headers/Path]
        Agent3[Pod 3 Traffic Agent<br/>Inspects Headers/Path]
    end

    subgraph "Filter Decision"
        Filter1{Match?}
        Filter2{Match?}
        Filter3{Match?}
    end

    subgraph "Destinations"
        Dev[Developer<br/>~10% of total]
        LocalApp1[Local App Container 1<br/>~30% of total]
        LocalApp2[Local App Container 2<br/>~30% of total]
        LocalApp3[Local App Container 3<br/>~30% of total]
    end

    TotalTraffic -->|33% to Pod 1| Agent1
    TotalTraffic -->|33% to Pod 2| Agent2
    TotalTraffic -->|33% to Pod 3| Agent3

    Agent1 --> Filter1
    Agent2 --> Filter2
    Agent3 --> Filter3

    Filter1 -->|10% match<br/>x-intercept: dev| Dev
    Filter1 -->|90% no match| LocalApp1
    Filter2 -->|10% match<br/>x-intercept: dev| Dev
    Filter2 -->|90% no match| LocalApp2
    Filter3 -->|10% match<br/>x-intercept: dev| Dev
    Filter3 -->|90% no match| LocalApp3

    style Agent1 fill:#ffcc99
    style Agent2 fill:#ffcc99
    style Agent3 fill:#ffcc99
    style Dev fill:#ff99ff
    style Filter1 fill:#ffffcc
    style Filter2 fill:#ffffcc
    style Filter3 fill:#ffffcc
```

**With Filters Example (CORRECTED):**
- 3 pods, **ALL 3 intercepted** (not just 1)
- Filter: `x-intercept: dev` (matches 10% of requests)
- **ALL pods** inspect headers and apply the same filter
- Developer receives: (33% × 10%) + (33% × 10%) + (33% × 10%) = **10% of total traffic**
- Pod 1's app container: 33% × 90% = **29.7% of total traffic**
- Pod 2's app container: 33% × 90% = **29.7% of total traffic**
- Pod 3's app container: 33% × 90% = **29.7% of total traffic**
- **Total to developer: 10%**
- **Total to cluster: 90%**

---

## Summary: How Telepresence Handles HPA + Ingress - CORRECTED

### What Telepresence Does NOT Modify:
✅ **Service** - ClusterIP and load balancing unchanged
✅ **Ingress** - Routing rules unchanged
✅ **Endpoints** - Kubernetes endpoints list unchanged
✅ **Network Policies** - Security policies unchanged

### What Telepresence DOES Modify:
🔧 **Pod Specs** - Injects Traffic Agent sidecar via mutating webhook
🔧 **Traffic Routing** - **ALL agents in the workload** intercept and forward traffic
🔧 **DNS Resolution** - Agents can proxy DNS for cluster awareness

### Key Behaviors with HPA (CORRECTED):
1. **Scale UP**: New pods get agents, **ALL pods receive the same intercept** (broadcast)
2. **Scale DOWN**: Pods are removed but remaining pods continue intercepting
3. **Traffic Distribution**: Service load-balances across all pods (1/N per pod)
4. **Intercept Broadcast**: **ALL pods** in workload receive same intercept via `WatchIntercepts` filter
5. **Shared ClientSession**: **ALL agents** forward to the same ClientSession ID
6. **Result**: **100% of traffic reaches developer** (not 1/N)
7. **HTTP Filtering**: The **ONLY way** to reduce traffic to developer
8. **Session Management**: Each pod's agent is tracked by unique pod UID
9. **Automatic Recovery**: Intercepts automatically restore after pod restarts

### Best Practices (UPDATED):
- ⚠️ **Use HTTP filtering** - Without it, developer receives **100% of production traffic**
- ⚠️ **Be careful in production** - Basic intercepts route ALL traffic to your machine
- Monitor **HPA metrics** - All intercepted pods still contribute to scaling decisions
- Consider **minimum replicas** ≥ 2 if using HTTP filtering for partial traffic
- Use **preview URLs** for completely isolated testing
- **HTTP header/path filters** are essential for production safety
- Test bandwidth - receiving 100% of prod traffic can be overwhelming

---

## Rendering These Diagrams

### Online Renderers:
- [Mermaid Live Editor](https://mermaid.live/) - Official editor
- [GitHub](https://github.com) - Native support in markdown
- [GitLab](https://gitlab.com) - Native support in markdown

### VS Code Extensions:
- **Markdown Preview Mermaid Support** - Preview in VS Code
- **Mermaid Editor** - Dedicated Mermaid editor

### Export Options:
- SVG (vector graphics - best for documentation)
- PNG (raster graphics - good for presentations)
- PDF (via browser print function)

### Integration Options:
- **Docusaurus** - Native Mermaid plugin
- **MkDocs** - Mermaid plugin available
- **Hugo** - Mermaid shortcode support
- **Jekyll** - Mermaid plugin available

---

## Customization Examples

### Color Schemes

Add to any diagram for custom styling:

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#ff6b6b','primaryTextColor':'#fff','primaryBorderColor':'#c92a2a','lineColor':'#495057','secondaryColor':'#74c0fc','tertiaryColor':'#ffd43b'}}}%%
```

### Dark Mode

```mermaid
%%{init: {'theme':'dark'}}%%
```

### Sequence Diagram Styling

```mermaid
%%{init: {'sequence': {'actorMargin':50, 'boxMargin':10, 'messageMargin':40}}}%%
```

---

*Generated for Telepresence documentation - HPA + Ingress traffic handling*

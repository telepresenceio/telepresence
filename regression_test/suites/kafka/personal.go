package kafka

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

const (
	brokerName   = "kafka-broker"
	appName      = "kafka-checkout"
	splitName    = "orders"
	sourceTopic  = "rtest-orders"
	sourceGroup  = "rtest-checkout"
	topicEnv     = "ORDERS_TOPIC"
	groupEnv     = "KAFKA_GROUP"
	isolationEnv = "KAFKA_ISOLATION_LEVEL"

	secondSplitName    = "payments"
	secondSourceTopic  = "rtest-payments"
	secondSourceGroup  = "rtest-payments-checkout"
	secondTopicEnv     = "PAYMENTS_TOPIC"
	secondGroupEnv     = "PAYMENTS_KAFKA_GROUP"
	secondIsolationEnv = "PAYMENTS_KAFKA_ISOLATION_LEVEL"
	paymentsAppTopic   = "rtest-payments-app"
	paymentsAppGroup   = "rtest-payments-app-group"
	providerSecretName = "kafka-provider-credentials"
	providerUsername   = "provider"
	providerPassword   = "provider-secret"

	argoNamespace          = "rtest-kafka-argo"
	argoInstallURL         = "https://github.com/argoproj/argo-rollouts/releases/download/v1.10.0/install.yaml"
	argoController         = "argo-rollouts"
	argoClusterRoleBinding = "argo-rollouts"
)

type splitFixture struct {
	name, topic, group, topicEnv, groupEnv, isolationEnv, transactionalIDEnv string
}

type kafkaTLSMaterial struct {
	ca, certificate, privateKey string
}

type Personal struct {
	rt.Suite
}

func init() {
	rt.Register(&Personal{},
		rt.InArea("kafka"),
		rt.NeedsManager(managers.Kafka()),
		rt.WithLabels(rt.Slow),
	)
}

func (s *Personal) Test_TransactionalPersonalRoutes() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	brokerAddress := brokerName + "." + ns + ":9092"
	secureBrokerAddress := brokerName + "." + ns + ":9094"
	tlsMaterial := makeKafkaTLSMaterial(t, brokerName, brokerName+"."+ns)
	s.Workload(kafkaBroker(brokerAddress, secureBrokerAddress, tlsMaterial))
	app := s.Workload(kafkaApplication())
	conn := s.Connect()
	client := openKafka(t, ctx, brokerAddress)
	createTopic(t, ctx, client, sourceTopic, nil)
	createTopic(t, ctx, client, secondSourceTopic, nil)
	minusOne := "-1"
	deletePolicy := "delete"
	shadowConfigs := map[string]*string{
		"cleanup.policy": &deletePolicy, "retention.ms": &minusOne, "retention.bytes": &minusOne,
	}
	for _, topic := range []string{paymentsAppTopic, "rtest-payments-session-0", "rtest-payments-session-1"} {
		createTopic(t, ctx, client, topic, shadowConfigs)
	}
	apply(t, ctx, s.R(), ns, "kafka-provider-credentials", kafkaProviderSecret(ns, tlsMaterial.ca))
	ordersSplit := splitFixture{
		splitName, sourceTopic, sourceGroup, topicEnv, groupEnv, isolationEnv, "KAFKA_TRANSACTIONAL_ID",
	}
	paymentsSplit := splitFixture{
		secondSplitName, secondSourceTopic, secondSourceGroup,
		secondTopicEnv, secondGroupEnv, secondIsolationEnv, "PAYMENTS_KAFKA_TRANSACTIONAL_ID",
	}

	apply(t, ctx, s.R(), ns, "kafka-split-orders", kafkaSplit(ns, brokerAddress, appName, ordersSplit))
	t.Cleanup(func() { cleanupSplit(s.R(), ns, appName, splitName) })
	apply(t, ctx, s.R(), ns, "kafka-split-payments",
		kafkaPreprovisionedSplit(ns, secureBrokerAddress, brokerName+"."+ns, appName, paymentsSplit))
	t.Cleanup(func() { cleanupSplit(s.R(), ns, appName, secondSplitName) })
	waitSplitPhase(t, ctx, s.R(), ns, splitName, "Enabled")
	waitSplitPhase(t, ctx, s.R(), ns, secondSplitName, "Enabled")

	local := s.LocalEcho()
	alice := conn.InterceptNamed(t, "alice", app,
		rt.ToLocal(local, "http"), cli.MountFalse(), cli.KafkaHeader("tenant", "blue"))
	defer func() {
		if alice != nil {
			alice.Detach(t)
		}
	}()
	bob := conn.InterceptNamed(t, "bob", app, cli.KafkaOnly(), cli.KafkaHeader("tenant", "green"))
	defer func() {
		if bob != nil {
			bob.Detach(t)
		}
	}()
	requireKafkaRoutes(t, alice, false, ordersSplit, paymentsSplit)
	requireKafkaRoutes(t, bob, true, ordersSplit, paymentsSplit)
	rt.RoutedToLocal(t, app.ServiceURL(), local)

	appEnv := applicationEnvironment(t, ctx, s.R(), ns)
	produce(t, ctx, client,
		&kgo.Record{Topic: sourceTopic, Key: []byte("alice"), Value: []byte("blue"), Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("blue")}}},
		&kgo.Record{Topic: sourceTopic, Key: []byte("bob"), Value: []byte("green"), Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("green")}}},
		&kgo.Record{Topic: sourceTopic, Key: []byte("application"), Value: []byte("plain")},
		&kgo.Record{Topic: secondSourceTopic, Key: []byte("alice-payment"), Value: []byte("blue"), Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("blue")}}},
		&kgo.Record{Topic: secondSourceTopic, Key: []byte("bob-payment"), Value: []byte("green"), Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("green")}}},
		&kgo.Record{Topic: secondSourceTopic, Key: []byte("application-payment"), Value: []byte("plain")},
	)
	requireRecord(t, ctx, brokerAddress, alice.Intercept.Environment, ordersSplit, "alice")
	requireRecord(t, ctx, brokerAddress, bob.Intercept.Environment, ordersSplit, "bob")
	requireRecord(t, ctx, brokerAddress, appEnv, ordersSplit, "application")
	requireRecord(t, ctx, brokerAddress, alice.Intercept.Environment, paymentsSplit, "alice-payment")
	requireRecord(t, ctx, brokerAddress, bob.Intercept.Environment, paymentsSplit, "bob-payment")
	requireRecord(t, ctx, brokerAddress, appEnv, paymentsSplit, "application-payment")

	orders := getSplit(t, ctx, s.R(), ns, splitName)
	managerNamespace := s.Manager().Namespace
	_, err := s.R().Kubectl(ctx, managerNamespace, "rollout", "restart", "statefulset/"+orders.Status.SplitterName)
	s.Require().NoError(err)
	_, err = s.R().Kubectl(ctx, managerNamespace, "rollout", "status", "statefulset/"+orders.Status.SplitterName, "--timeout=180s")
	s.Require().NoError(err)
	produce(t, ctx, client, &kgo.Record{
		Topic: sourceTopic, Key: []byte("after-splitter-restart"), Value: []byte("blue"),
		Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("blue")}},
	})
	requireRecord(t, ctx, brokerAddress, alice.Intercept.Environment, ordersSplit, "after-splitter-restart")

	produce(t, ctx, client, &kgo.Record{
		Topic: sourceTopic, Key: []byte("alice-residue"), Value: []byte("blue"),
		Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("blue")}},
	})
	waitForRecord(t, ctx, brokerAddress, alice.Intercept.Environment[topicEnv], "alice-residue")

	env := rt.Env{Ctx: ctx, T: t, R: s.R()}
	s.Require().NoError(rt.RestartManager(env))
	s.Require().Eventually(func() bool {
		var entries []cli.ListEntry
		if err := s.CLI().JSON(ctx, &entries, "list", "--format", "json"); err != nil {
			return false
		}
		for _, entry := range entries {
			if entry.Name == appName && len(entry.InterceptInfo) == 2 {
				return true
			}
		}
		return false
	}, 2*time.Minute, time.Second)

	apply(t, ctx, s.R(), ns, "kafka-hpa", kafkaHPA(ns))
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = s.R().Kubectl(cleanupCtx, ns, "delete", "hpa/"+appName, "--ignore-not-found", "--wait=false")
	})
	waitRedirectedPods(t, ctx, s.R(), ns, 2)
	_, err = s.R().Kubectl(ctx, managerNamespace, "rollout", "restart", "deployment/tp-kafka")
	s.Require().NoError(err)
	_, err = s.R().Kubectl(ctx, managerNamespace, "rollout", "status", "deployment/tp-kafka", "--timeout=120s")
	s.Require().NoError(err)
	waitSplitPhase(t, ctx, s.R(), ns, splitName, "Enabled")
	waitSplitPhase(t, ctx, s.R(), ns, secondSplitName, "Enabled")
	produce(t, ctx, client, &kgo.Record{
		Topic: sourceTopic, Key: []byte("after-controller-restart"), Value: []byte("green"),
		Headers: []kgo.RecordHeader{{Key: "tenant", Value: []byte("green")}},
	})
	requireRecord(t, ctx, brokerAddress, bob.Intercept.Environment, ordersSplit, "after-controller-restart")

	alice.DetachWithin(t, 2*time.Minute, time.Second)
	alice = nil
	requireRecord(t, ctx, brokerAddress, appEnv, ordersSplit, "alice-residue")
	bob.DetachWithin(t, 2*time.Minute, time.Second)
	bob = nil
	waitAllRoutesGone(t, ctx, s.R(), ns)

	exact := conn.InterceptNamed(t, "exact-key", app, cli.KafkaOnly(), cli.KafkaKey("exact"))
	prefix := conn.InterceptNamed(t, "prefix-key", app, cli.KafkaOnly(), cli.KafkaKeyPrefix("prefix-"))
	produce(t, ctx, client,
		&kgo.Record{Topic: sourceTopic, Key: []byte("exact"), Value: []byte("exact")},
		&kgo.Record{Topic: sourceTopic, Key: []byte("prefix-one"), Value: []byte("prefix")},
	)
	requireRecord(t, ctx, brokerAddress, exact.Intercept.Environment, ordersSplit, "exact")
	requireRecord(t, ctx, brokerAddress, prefix.Intercept.Environment, ordersSplit, "prefix-one")
	exact.Detach(t)
	prefix.Detach(t)
	waitAllRoutesGone(t, ctx, s.R(), ns)

	apply(t, ctx, s.R(), ns, "kafka-expiring-route", kafkaExpiringRoute(ns))
	expiringEnvironment := waitRouteReady(t, ctx, s.R(), ns, "expiring")
	produce(t, ctx, client, &kgo.Record{Topic: sourceTopic, Key: []byte("expiry-residue"), Value: []byte("expiry")})
	waitForRecord(t, ctx, brokerAddress, expiringEnvironment[topicEnv], "expiry-residue")
	expired := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	_, err = s.R().Kubectl(ctx, ns, "patch", "kroute/expiring", "--type=merge", "-p",
		fmt.Sprintf(`{"spec":{"expiresAt":%q}}`, expired))
	s.Require().NoError(err)
	waitRouteGone(t, ctx, s.R(), ns, "expiring")
	requireRecord(t, ctx, brokerAddress, appEnv, ordersSplit, "expiry-residue")

	networkOnly := conn.InterceptNamed(t, "network-only", app,
		rt.ToLocal(local, "http"), cli.MountFalse(), cli.NoKafka())
	if len(networkOnly.Intercept.KafkaSplits) != 0 {
		t.Fatalf("--no-kafka attached Kafka splits: %v", networkOnly.Intercept.KafkaSplits)
	}
	rt.RoutedToLocal(t, app.ServiceURL(), local)
	networkOnly.Detach(t)
	apply(t, ctx, s.R(), ns, "kafka-pdb", kafkaPDB(ns))
	for _, name := range []string{splitName, secondSplitName} {
		_, err := s.R().Kubectl(ctx, ns, "patch", "ksplit/"+name, "--type=merge", "-p", `{"spec":{"desiredState":"Disabled"}}`)
		s.Require().NoError(err)
	}
	waitSplitReason(t, ctx, s.R(), ns, splitName, "RestoringApplication")
	_, err = s.R().Kubectl(ctx, ns, "delete", "pdb/"+appName, "--wait")
	s.Require().NoError(err)
	waitSplitPhase(t, ctx, s.R(), ns, splitName, "Disabled")
	waitSplitPhase(t, ctx, s.R(), ns, secondSplitName, "Disabled")
}

func (s *Personal) Test_WorkloadAdapters() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	brokerAddress := brokerName + "." + ns + ":9092"
	s.Workload(kafkaBroker(brokerAddress, "", nil))
	conn := s.Connect()
	client := openKafka(t, ctx, brokerAddress)
	ensureArgoRollouts(t, ctx, s.R())
	adapters := []struct {
		kind     string
		template workloads.Template
	}{
		{kind: "StatefulSet", template: workloads.EchoStatefulSet("kafka-stateful")},
		{kind: "ReplicaSet", template: workloads.EchoReplicaSet("kafka-replicaset")},
		{kind: "Rollout", template: workloads.EchoRollout("kafka-rollout")},
	}
	for _, adapter := range adapters {
		name := adapter.template.Name
		if adapter.kind == "StatefulSet" {
			adapter.template.NoService = true
		}
		fixture := splitFixture{
			name: name, topic: "rtest-" + name, group: "rtest-" + name,
			topicEnv: topicEnv, groupEnv: groupEnv, isolationEnv: isolationEnv,
			transactionalIDEnv: "KAFKA_TRANSACTIONAL_ID",
		}
		adapter.template.Env = map[string]string{
			topicEnv: fixture.topic, groupEnv: fixture.group, isolationEnv: "read_uncommitted",
			"KAFKA_TRANSACTIONAL_ID": name,
		}
		s.Workload(adapter.template)
		createTopic(t, ctx, client, fixture.topic, nil)
		apply(t, ctx, s.R(), ns, "kafka-split-"+name, kafkaSplit(ns, brokerAddress, name, fixture))
		t.Cleanup(func() { cleanupSplit(s.R(), ns, name, name) })
		waitSplitPhase(t, ctx, s.R(), ns, name, "Enabled")
		waitWorkloadRedirected(t, ctx, s.R(), ns, name, fixture)
		if adapter.kind == "StatefulSet" {
			attachment := conn.InterceptNamed(t, "queue-only", s.Workload(adapter.template),
				cli.KafkaOnly(), cli.KafkaHeader("adapter", "stateful"))
			requireKafkaRoutes(t, attachment, true, fixture)
			attachment.Detach(t)
		}
		_, err := s.R().Kubectl(ctx, ns, "patch", "ksplit/"+name, "--type=merge", "-p", `{"spec":{"desiredState":"Disabled"}}`)
		s.Require().NoError(err, adapter.kind)
		waitSplitPhase(t, ctx, s.R(), ns, name, "Disabled")
	}
}

func ensureArgoRollouts(t testing.TB, ctx context.Context, r *rt.Runtime) {
	t.Helper()
	if _, err := r.Kubectl(ctx, "", "create", "namespace", argoNamespace); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "already exists") {
		t.Fatalf("create namespace %s: %v", argoNamespace, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = r.Kubectl(cleanupCtx, "", "delete", "namespace", argoNamespace, "--ignore-not-found", "--wait=false")
	})
	if _, err := r.Kubectl(ctx, argoNamespace, "apply", "--server-side", "--force-conflicts", "-f", argoInstallURL); err != nil {
		t.Fatalf("install Argo Rollouts: %v", err)
	}
	patch := fmt.Sprintf(`[{"op":"replace","path":"/subjects/0/namespace","value":%q}]`, argoNamespace)
	if _, err := r.Kubectl(ctx, "", "patch", "clusterrolebinding", argoClusterRoleBinding,
		"--type=json", "-p", patch); err != nil {
		t.Fatalf("patch Argo Rollouts binding: %v", err)
	}
	if _, err := r.Kubectl(ctx, argoNamespace, "rollout", "status", "deployment/"+argoController, "--timeout=120s"); err != nil {
		t.Fatalf("wait for Argo Rollouts: %v", err)
	}
}

func kafkaBroker(address, secureAddress string, tlsMaterial *kafkaTLSMaterial) workloads.Template {
	template := workloads.Template{
		Name: brokerName, Kind: "Deployment", Replicas: 1,
		Image: "apache/kafka@sha256:c89f315cff967322c5d2021434b32271393cb193aa7ec1d43e97341924e57069",
		Port:  9092, SvcName: brokerName,
		Env: map[string]string{
			"CLUSTER_ID":                                     "MkU3OEVBNTcwNTJENDM2Qk",
			"KAFKA_ADVERTISED_LISTENERS":                     "PLAINTEXT://" + address,
			"KAFKA_CONTROLLER_LISTENER_NAMES":                "CONTROLLER",
			"KAFKA_CONTROLLER_QUORUM_VOTERS":                 "1@localhost:9093",
			"KAFKA_GROUP_INITIAL_REBALANCE_DELAY_MS":         "0",
			"KAFKA_INTER_BROKER_LISTENER_NAME":               "PLAINTEXT",
			"KAFKA_LISTENERS":                                "PLAINTEXT://:9092,CONTROLLER://:9093",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":           "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
			"KAFKA_NODE_ID":                                  "1",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "1",
			"KAFKA_PROCESS_ROLES":                            "broker,controller",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR":            "1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "1",
		},
	}
	if tlsMaterial != nil {
		template.ExtraPorts = []workloads.NamedPort{{Name: "sasl-tls", Port: 9094}}
		template.ConfigVolume = workloads.ConfigVolume{
			Name: "kafka-server-jaas", Key: "server-jaas.conf", MountPath: "/etc/kafka/jaas",
			Content: `KafkaServer {
  org.apache.kafka.common.security.plain.PlainLoginModule required
  username="admin"
  password="admin-secret"
  user_admin="admin-secret"
  user_provider="provider-secret";
};`,
		}
		template.Env["KAFKA_ADVERTISED_LISTENERS"] += ",SECURE://" + secureAddress
		template.Env["KAFKA_LISTENERS"] = "PLAINTEXT://:9092,SECURE://:9094,CONTROLLER://:9093"
		template.Env["KAFKA_LISTENER_SECURITY_PROTOCOL_MAP"] = "CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,SECURE:SASL_SSL"
		template.Env["KAFKA_SASL_ENABLED_MECHANISMS"] = "PLAIN"
		template.Env["KAFKA_OPTS"] = "-Djava.security.auth.login.config=/etc/kafka/jaas/server-jaas.conf"
		template.Env["KAFKA_SSL_CLIENT_AUTH"] = "none"
		template.Env["KAFKA_SSL_KEYSTORE_TYPE"] = "PEM"
		template.Env["KAFKA_SSL_KEYSTORE_CERTIFICATE_CHAIN"] = strings.ReplaceAll(tlsMaterial.certificate, "\n", `\n`)
		template.Env["KAFKA_SSL_KEYSTORE_KEY"] = strings.ReplaceAll(tlsMaterial.privateKey, "\n", `\n`)
	}
	return template
}

func makeKafkaTLSMaterial(t testing.TB, dnsNames ...string) *kafkaTLSMaterial {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: dnsNames[0]}, DNSNames: dnsNames,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		t.Fatal(err)
	}
	return &kafkaTLSMaterial{
		ca:          string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})),
		privateKey:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})),
	}
}

func kafkaProviderSecret(namespace, ca string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: Opaque
data:
  ca.crt: %s
  username: %s
  password: %s
`, providerSecretName, namespace,
		base64.StdEncoding.EncodeToString([]byte(ca)),
		base64.StdEncoding.EncodeToString([]byte(providerUsername)),
		base64.StdEncoding.EncodeToString([]byte(providerPassword)))
}

func kafkaApplication() workloads.Template {
	template := workloads.Echo(appName)
	template.Env = map[string]string{
		topicEnv: sourceTopic, groupEnv: sourceGroup, isolationEnv: "read_uncommitted", "KAFKA_TRANSACTIONAL_ID": "checkout",
		secondTopicEnv: secondSourceTopic, secondGroupEnv: secondSourceGroup, secondIsolationEnv: "read_uncommitted",
		"PAYMENTS_KAFKA_TRANSACTIONAL_ID": "checkout-payments",
	}
	return template
}

func kafkaSplit(namespace, brokerAddress, workload string, fixture splitFixture) string {
	return fmt.Sprintf(`apiVersion: kafka.telepresence.io/v1alpha1
kind: KafkaSplit
metadata:
  name: %s
  namespace: %s
spec:
  desiredState: Enabled
  workloadSelector:
    matchLabels:
      app: %s
  container: %s
  connection:
    bootstrapServers: [%q]
  source:
    group: %s
    topics: [%s]
    offsetReset: earliest
  application:
    topicEnv: %s
    topicSeparator: ","
    groupEnv: %s
    isolationLevelEnv: %s
    transactionalIDEnv: %s
  splitter:
    replicas: 2
    batchSize: 20
  shadows:
    mode: Managed
    managed: {}
`, fixture.name, namespace, workload, workload, brokerAddress, fixture.group, fixture.topic,
		fixture.topicEnv, fixture.groupEnv, fixture.isolationEnv, fixture.transactionalIDEnv)
}

func kafkaPreprovisionedSplit(
	namespace, brokerAddress, serverName, workload string,
	fixture splitFixture,
) string {
	return fmt.Sprintf(`apiVersion: kafka.telepresence.io/v1alpha1
kind: KafkaSplit
metadata:
  name: %s
  namespace: %s
spec:
  desiredState: Enabled
  workloadSelector:
    matchLabels:
      app: %s
  container: %s
  connection:
    bootstrapServers: [%q]
    tls:
      ca:
        name: %s
        key: ca.crt
      serverName: %s
    sasl:
      mechanism: PLAIN
      username:
        secretKeyRef:
          name: %s
          key: username
      password:
        secretKeyRef:
          name: %s
          key: password
  source:
    group: %s
    topics: [%s]
    offsetReset: earliest
  application:
    topicEnv: %s
    topicSeparator: ","
    groupEnv: %s
    isolationLevelEnv: %s
    transactionalIDEnv: %s
  splitter:
    replicas: 2
    batchSize: 20
  shadows:
    mode: Preprovisioned
    preprovisioned:
      applicationTopics:
        %s: %s
      applicationGroup: %s
      sessions:
        - name: slot-0
          topics:
            %s: rtest-payments-session-0
          group: rtest-payments-session-group-0
        - name: slot-1
          topics:
            %s: rtest-payments-session-1
          group: rtest-payments-session-group-1
`, fixture.name, namespace, workload, workload, brokerAddress,
		providerSecretName, serverName, providerSecretName, providerSecretName,
		fixture.group, fixture.topic,
		fixture.topicEnv, fixture.groupEnv, fixture.isolationEnv, fixture.transactionalIDEnv,
		fixture.topic, paymentsAppTopic, paymentsAppGroup, fixture.topic, fixture.topic)
}

func kafkaHPA(namespace string) string {
	return fmt.Sprintf(`apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: %s
  namespace: %s
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: %s
  minReplicas: 2
  maxReplicas: 2
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          type: Utilization
          averageUtilization: 50
`, appName, namespace, appName)
}

func kafkaPDB(namespace string) string {
	return fmt.Sprintf(`apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: %s
  namespace: %s
spec:
  maxUnavailable: 0
  selector:
    matchLabels:
      app: %s
`, appName, namespace, appName)
}

func kafkaExpiringRoute(namespace string) string {
	return fmt.Sprintf(`apiVersion: kafka.telepresence.io/v1alpha1
kind: KafkaRoute
metadata:
  name: expiring
  namespace: %s
spec:
  splitRef:
    name: %s
  attachmentID: expiry-regression
  sessionID: expired-client
  expiresAt: %s
  desiredState: Active
`, namespace, splitName, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
}

func apply(t testing.TB, ctx context.Context, r *rt.Runtime, namespace, name, manifest string) {
	t.Helper()
	if err := r.ApplyManifest(ctx, namespace, name, manifest); err != nil {
		t.Fatal(err)
	}
}

func openKafka(t testing.TB, ctx context.Context, address string) *kgo.Client {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(address), kgo.ClientID("telepresence-regression"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := client.Ping(ctx); err == nil {
			return client
		} else if time.Now().After(deadline) {
			t.Fatalf("Kafka broker did not become ready: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func createTopic(t testing.TB, ctx context.Context, client *kgo.Client, topic string, configs map[string]*string) {
	t.Helper()
	responses, err := kadm.NewClient(client).CreateTopics(ctx, 1, 1, configs, topic)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := responses[topic]
	if !ok || (response.Err != nil && !errors.Is(response.Err, kerr.TopicAlreadyExists)) {
		t.Fatalf("create topic %s: %+v", topic, response)
	}
}

func produce(t testing.TB, ctx context.Context, client *kgo.Client, records ...*kgo.Record) {
	t.Helper()
	if err := client.ProduceSync(ctx, records...).FirstErr(); err != nil {
		t.Fatal(err)
	}
}

func requireRecord(
	t testing.TB,
	ctx context.Context,
	broker string,
	environment map[string]string,
	fixture splitFixture,
	key string,
) {
	t.Helper()
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(broker), kgo.ConsumerGroup(environment[fixture.groupEnv]),
		kgo.ConsumeTopics(environment[fixture.topicEnv]),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()), kgo.FetchIsolationLevel(kgo.ReadCommitted()), kgo.DisableAutoCommit(),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	pollCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for {
		fetches := consumer.PollFetches(pollCtx)
		if err := fetches.Err(); err != nil {
			t.Fatalf("consume %s: %v", key, err)
		}
		for _, record := range fetches.Records() {
			if string(record.Key) != key {
				t.Fatalf("consumer for %s received record for %s", key, record.Key)
			}
			if err := consumer.CommitRecords(pollCtx, record); err != nil {
				t.Fatal(err)
			}
			return
		}
		if pollCtx.Err() != nil {
			t.Fatalf("record %s was not routed: %v", key, pollCtx.Err())
		}
	}
}

func waitForRecord(t testing.TB, ctx context.Context, broker, topic, key string) {
	t.Helper()
	consumer, err := kgo.NewClient(
		kgo.SeedBrokers(broker),
		kgo.ConsumePartitions(map[string]map[int32]kgo.Offset{topic: {0: kgo.NewOffset().AtStart()}}),
		kgo.FetchIsolationLevel(kgo.ReadCommitted()),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	pollCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	for pollCtx.Err() == nil {
		fetches := consumer.PollFetches(pollCtx)
		if err := fetches.Err(); err != nil {
			t.Fatalf("consume %s: %v", key, err)
		}
		if slices.ContainsFunc(fetches.Records(), func(record *kgo.Record) bool { return string(record.Key) == key }) {
			return
		}
	}
	t.Fatalf("record %s did not reach topic %s: %v", key, topic, pollCtx.Err())
}

func requireKafkaRoutes(t testing.TB, attachment *rt.Attach, only bool, fixtures ...splitFixture) {
	t.Helper()
	if attachment.Intercept == nil {
		t.Fatal("intercept response is missing")
	}
	if attachment.Intercept.KafkaOnly != only {
		t.Fatalf("kafka_only = %v, want %v", attachment.Intercept.KafkaOnly, only)
	}
	names := make([]string, len(fixtures))
	for i, fixture := range fixtures {
		names[i] = fixture.name
	}
	slices.Sort(names)
	if !slices.Equal(attachment.Intercept.KafkaSplits, names) {
		t.Fatalf("Kafka splits = %v", attachment.Intercept.KafkaSplits)
	}
	for _, fixture := range fixtures {
		for _, name := range []string{fixture.topicEnv, fixture.groupEnv, fixture.isolationEnv} {
			if attachment.Intercept.Environment[name] == "" {
				t.Fatalf("Kafka environment is missing %s", name)
			}
		}
	}
}

func applicationEnvironment(t testing.TB, ctx context.Context, r *rt.Runtime, namespace string) map[string]string {
	t.Helper()
	pods := new(corev1.PodList)
	if err := r.KubectlJSON(ctx, namespace, pods, "get", "pods", "-l", "app="+appName); err != nil {
		t.Fatal(err)
	}
	for i := range pods.Items {
		for _, container := range pods.Items[i].Spec.Containers {
			if container.Name != appName {
				continue
			}
			env := make(map[string]string)
			for _, variable := range container.Env {
				env[variable.Name] = variable.Value
			}
			return env
		}
	}
	t.Fatal("application container was not found")
	return nil
}

// waitFor polls poll once a second until it reports done, or fails the test
// with its last state after timeout elapses. poll may call t.Fatal itself
// for errors that should abort immediately rather than be retried.
func waitFor(t testing.TB, poll func() (done bool, state string)) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		done, state := poll()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s", state)
		}
		time.Sleep(time.Second)
	}
}

func waitRedirectedPods(t testing.TB, ctx context.Context, r *rt.Runtime, namespace string, count int) {
	t.Helper()
	waitFor(t, func() (bool, string) {
		pods := new(corev1.PodList)
		if err := r.KubectlJSON(ctx, namespace, pods, "get", "pods", "-l", "app="+appName); err != nil {
			t.Fatal(err)
		}
		ready := 0
		for i := range pods.Items {
			if !podReady(&pods.Items[i]) {
				continue
			}
			for _, container := range pods.Items[i].Spec.Containers {
				if container.Name == appName &&
					envValue(container.Env, topicEnv) != sourceTopic &&
					envValue(container.Env, secondTopicEnv) != secondSourceTopic {
					ready++
				}
			}
		}
		return ready == count, fmt.Sprintf("redirected ready pods = %d, want %d", ready, count)
	})
}

func waitWorkloadRedirected(
	t testing.TB,
	ctx context.Context,
	r *rt.Runtime,
	namespace, workload string,
	fixture splitFixture,
) {
	t.Helper()
	waitFor(t, func() (bool, string) {
		pods := new(corev1.PodList)
		if err := r.KubectlJSON(ctx, namespace, pods, "get", "pods", "-l", "app="+workload); err != nil {
			t.Fatal(err)
		}
		for i := range pods.Items {
			if !podReady(&pods.Items[i]) {
				continue
			}
			for _, container := range pods.Items[i].Spec.Containers {
				if container.Name == workload && envValue(container.Env, fixture.topicEnv) != fixture.topic {
					return true, ""
				}
			}
		}
		return false, fmt.Sprintf("%s did not produce a redirected ready Pod", workload)
	})
}

func podReady(pod *corev1.Pod) bool {
	return slices.ContainsFunc(pod.Status.Conditions, func(condition corev1.PodCondition) bool {
		return condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue
	})
}

func envValue(environment []corev1.EnvVar, name string) string {
	for _, variable := range environment {
		if variable.Name == name {
			return variable.Value
		}
	}
	return ""
}

func waitSplitPhase(t testing.TB, ctx context.Context, r *rt.Runtime, namespace, name string, phase api.SplitPhase) {
	t.Helper()
	waitForSplit(t, ctx, r, namespace, name, func(split *api.KafkaSplit) bool { return split.Status.Phase == phase }, "phase "+string(phase))
}

func getSplit(t testing.TB, ctx context.Context, r *rt.Runtime, namespace, name string) *api.KafkaSplit {
	t.Helper()
	split := new(api.KafkaSplit)
	if err := r.KubectlJSON(ctx, namespace, split, "get", "ksplit/"+name); err != nil {
		t.Fatal(err)
	}
	return split
}

func waitSplitReason(t testing.TB, ctx context.Context, r *rt.Runtime, namespace, name, reason string) {
	t.Helper()
	waitForSplit(t, ctx, r, namespace, name, func(split *api.KafkaSplit) bool {
		return slices.ContainsFunc(split.Status.Conditions, func(condition metav1.Condition) bool { return condition.Reason == reason })
	}, "reason "+reason)
}

func waitRouteReady(t testing.TB, ctx context.Context, r *rt.Runtime, namespace, name string) map[string]string {
	t.Helper()
	var environment map[string]string
	waitFor(t, func() (bool, string) {
		route := new(api.KafkaRoute)
		err := r.KubectlJSON(ctx, namespace, route, "get", "kroute/"+name)
		if err == nil && route.Status.Phase == "Ready" {
			environment = route.Status.Environment
			return true, ""
		}
		if err != nil {
			return false, fmt.Sprintf("KafkaRoute %s did not become ready: %v", name, err)
		}
		return false, fmt.Sprintf("KafkaRoute %s did not become ready: phase=%s conditions=%v", name, route.Status.Phase, route.Status.Conditions)
	})
	return environment
}

func waitRouteGone(t testing.TB, ctx context.Context, r *rt.Runtime, namespace, name string) {
	t.Helper()
	waitFor(t, func() (bool, string) {
		_, err := r.Kubectl(ctx, namespace, "get", "kroute/"+name)
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "not found") {
			return true, ""
		}
		return false, fmt.Sprintf("KafkaRoute %s was not removed after expiry: %v", name, err)
	})
}

func waitAllRoutesGone(t testing.TB, ctx context.Context, r *rt.Runtime, namespace string) {
	t.Helper()
	waitFor(t, func() (bool, string) {
		routes := new(api.KafkaRouteList)
		if err := r.KubectlJSON(ctx, namespace, routes, "get", "routes.kafka.telepresence.io"); err != nil {
			t.Fatal(err)
		}
		if len(routes.Items) == 0 {
			return true, ""
		}
		names := make([]string, len(routes.Items))
		for i := range routes.Items {
			names[i] = routes.Items[i].Name
		}
		return false, fmt.Sprintf("KafkaRoutes were not removed: %v", names)
	})
}

func waitForSplit(
	t testing.TB,
	ctx context.Context,
	r *rt.Runtime,
	namespace string,
	name string,
	ready func(*api.KafkaSplit) bool,
	description string,
) {
	t.Helper()
	waitFor(t, func() (bool, string) {
		split := new(api.KafkaSplit)
		err := r.KubectlJSON(ctx, namespace, split, "get", "ksplit/"+name)
		if err == nil && ready(split) {
			return true, ""
		}
		if err != nil {
			return false, fmt.Sprintf("KafkaSplit did not reach %s: %v", description, err)
		}
		return false, fmt.Sprintf("KafkaSplit did not reach %s: phase=%s conditions=%v", description, split.Status.Phase, split.Status.Conditions)
	})
}

func cleanupSplit(r *rt.Runtime, namespace, workload, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	_, _ = r.Kubectl(ctx, namespace, "delete", "pdb/"+workload, "--ignore-not-found", "--wait=false")
	_, _ = r.Kubectl(ctx, namespace, "patch", "ksplit/"+name, "--type=merge", "-p", `{"spec":{"desiredState":"Disabled"}}`)
	_, _ = r.Kubectl(ctx, namespace, "delete", "ksplit/"+name, "--ignore-not-found", "--wait", "--timeout=120s")
	routes := new(api.KafkaRouteList)
	if err := r.KubectlJSON(ctx, namespace, routes, "get", "routes.kafka.telepresence.io"); err == nil {
		for i := range routes.Items {
			if routes.Items[i].Spec.SplitRef.Name == name {
				_, _ = r.Kubectl(ctx, namespace, "delete", "kroute/"+routes.Items[i].Name, "--wait=false")
				_, _ = r.Kubectl(ctx, namespace, "patch", "kroute/"+routes.Items[i].Name, "--type=merge", "-p", `{"metadata":{"finalizers":[]}}`)
			}
		}
	}
	_, _ = r.Kubectl(ctx, namespace, "patch", "ksplit/"+name, "--type=merge", "-p", `{"metadata":{"finalizers":[]}}`)
}

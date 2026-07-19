package main

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestAgentWorkerConfigurationFailsClosed(t *testing.T) {
	if err := (config{}).validate(); err == nil {
		t.Fatal("empty AgentWorker configuration accepted")
	}
	if secureEndpoint("http://epoch.internal:8080", true) == nil {
		t.Fatal("remote plaintext dependency accepted")
	}
	if secureEndpoint("http://127.0.0.1:8080", true) != nil {
		t.Fatal("explicit loopback development endpoint rejected")
	}
	value := validDevelopmentConfig()
	if err := value.validate(); err != nil {
		t.Fatalf("valid development configuration: %v", err)
	}
	value.allowInsecure = false
	value.epochURL = "https://epoch.internal:8443"
	if err := value.validate(); err == nil || !strings.Contains(err.Error(), "production agent worker") {
		t.Fatalf("production transport downgrade error=%v", err)
	}
}

func TestAgentWorkerConfigurationRejectsUnsafeTimingAndCapacity(t *testing.T) {
	value := validDevelopmentConfig()
	value.ackWait = value.ackTimeout
	if value.validate() == nil {
		t.Fatal("ack wait equal to execution timeout accepted")
	}
	value = validDevelopmentConfig()
	value.executionLeaseTTL = value.ackTimeout - time.Second
	if value.validate() == nil {
		t.Fatal("execution lease shorter than message handling accepted")
	}
	value = validDevelopmentConfig()
	value.bucketTTL = value.completionTTL - time.Second
	if value.validate() == nil {
		t.Fatal("credit bucket lifetime shorter than provider completion accepted")
	}
	value = validDevelopmentConfig()
	value.maxAckPending = value.concurrency - 1
	if value.validate() == nil {
		t.Fatal("consumer capacity below concurrency accepted")
	}
	value = validDevelopmentConfig()
	value.byokSecretPrefix = "lites/providers/customer"
	if value.validate() == nil {
		t.Fatal("overlapping managed and BYOK secret namespaces accepted")
	}
}

func TestAgentWorkerPrivateProviderIsEngineeringTestOnly(t *testing.T) {
	value := validDevelopmentConfig()
	value.privateEngineeringProviderHost = "model-adapter.lites.test"
	value.providerRootCAFile = "local-ca.pem"
	if value.validate() == nil {
		t.Fatal("development environment accepted private provider")
	}
	value.environment = "engineering-test"
	if err := value.validate(); err != nil {
		t.Fatalf("engineering provider rejected: %v", err)
	}
	value.privateEngineeringProviderHost = "arbitrary.example"
	if value.validate() == nil {
		t.Fatal("arbitrary engineering private host accepted")
	}
}

func validDevelopmentConfig() config {
	return config{
		databaseURL: "postgres://localhost/lites", epochURL: "http://127.0.0.1:8080", natsURLs: []string{"nats://127.0.0.1:4222"},
		workerID: "agent-worker-test", promptPath: "prompts.json", promptHash: strings.Repeat("a", 64), routePath: "routes.json", routeHash: strings.Repeat("b", 64), toolPath: "tools.json", toolHash: strings.Repeat("c", 64), providerPath: "providers.json", providerHash: strings.Repeat("d", 64),
		executionIDKeyFile: "execution-id", executionLeasePepperFile: "execution-pepper", llmIDKeyFile: "llm-id", llmTokenPepperFile: "llm-pepper", billingIDKeyFile: "billing-id", agentIDKeyFile: "agent-id",
		s3Region: "us-east-1", payloadBucket: "payloads", s3Encryption: types.ServerSideEncryptionAes256,
		vaultAddress: "http://127.0.0.1:8200", vaultMount: "secret", vaultKeyPrefix: "lites/payload-keys", providerSecretPrefix: "lites/providers", byokSecretPrefix: "lites/byok",
		environment: "development", version: "test", region: "local", allowInsecure: true,
		streamReplicas: 1, concurrency: 4, maxDeliver: 20, maxAckPending: 8, maximumCommand: 1 << 20, maximumMessageBytes: 4 << 20, maximumMessages: 4096, maximumCalls: 16, maximumOutputTokens: 4096,
		ackWait: 25 * time.Minute, ackTimeout: 20 * time.Minute, pullExpires: 5 * time.Second, messageHeartbeat: 10 * time.Second, busyDelay: 5 * time.Second, retryDelay: 10 * time.Second,
		executionLeaseTTL: 22 * time.Minute, agentHeartbeat: 10 * time.Second, prepareTTL: 2 * time.Minute, completionTTL: 15 * time.Minute, reconciliationDelay: 2 * time.Minute, approvalTTL: 30 * time.Minute, bucketTTL: 20 * time.Minute, traceRatio: .1,
	}
}

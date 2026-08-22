// Package kafkaconfig resolves Kafka connection API settings into franz-go
// client options without exposing credentials to the traffic-manager.
package kafkaconfig

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/config"
	krbclient "github.com/jcmturner/gokrb5/v8/client"
	krbconfig "github.com/jcmturner/gokrb5/v8/config"
	"github.com/jcmturner/gokrb5/v8/keytab"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/aws"
	"github.com/twmb/franz-go/pkg/sasl/kerberos"
	"github.com/twmb/franz-go/pkg/sasl/oauth"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
	"golang.org/x/oauth2/clientcredentials"
	corev1 "k8s.io/api/core/v1"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	api "github.com/telepresenceio/telepresence/v2/pkg/kafkaintercept/api/v1alpha1"
)

// Options resolves connection and authentication settings in namespace.
func Options(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	connection api.KafkaConnectionSpec,
) ([]kgo.Opt, error) {
	opts := []kgo.Opt{kgo.SeedBrokers(connection.BootstrapServers...)}
	if connection.TLS != nil {
		tlsConfig, err := resolveTLS(ctx, reader, namespace, connection.TLS)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.DialTLSConfig(tlsConfig))
	}
	if connection.SASL != nil {
		mechanism, err := resolveSASL(ctx, reader, namespace, connection.SASL)
		if err != nil {
			return nil, err
		}
		opts = append(opts, kgo.SASL(mechanism))
	}
	return opts, nil
}

func resolveTLS(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	spec *api.KafkaTLSSpec,
) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: spec.ServerName}
	if spec.CA != nil {
		pem, err := secretKey(ctx, reader, namespace, spec.CA)
		if err != nil {
			return nil, fmt.Errorf("resolve Kafka TLS CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("Kafka TLS CA contains no PEM certificates")
		}
		config.RootCAs = pool
	}
	if spec.Certificate != nil {
		certificate, err := secretKey(ctx, reader, namespace, spec.Certificate)
		if err != nil {
			return nil, fmt.Errorf("resolve Kafka TLS certificate: %w", err)
		}
		privateKey, err := secretKey(ctx, reader, namespace, spec.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("resolve Kafka TLS private key: %w", err)
		}
		pair, err := tls.X509KeyPair(certificate, privateKey)
		if err != nil {
			return nil, fmt.Errorf("parse Kafka TLS client key pair: %w", err)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	return config, nil
}

func resolveSASL(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	spec *api.KafkaSASLSpec,
) (sasl.Mechanism, error) {
	switch spec.Mechanism {
	case "PLAIN":
		username, password, err := resolveUserPassword(ctx, reader, namespace, spec)
		if err != nil {
			return nil, err
		}
		return plain.Auth{User: username, Pass: password}.AsMechanism(), nil
	case "SCRAM-SHA-256", "SCRAM-SHA-512":
		username, password, err := resolveUserPassword(ctx, reader, namespace, spec)
		if err != nil {
			return nil, err
		}
		auth := scram.Auth{User: username, Pass: password}
		if spec.Mechanism == "SCRAM-SHA-256" {
			return auth.AsSha256Mechanism(), nil
		}
		return auth.AsSha512Mechanism(), nil
	case "OAUTHBEARER":
		return resolveOAuth(ctx, reader, namespace, spec.OAuth)
	case "GSSAPI":
		return resolveKerberos(ctx, reader, namespace, spec.Kerberos)
	case "AWS_MSK_IAM":
		return resolveAWS(ctx, spec.AWS)
	default:
		return nil, fmt.Errorf("unsupported Kafka SASL mechanism %q", spec.Mechanism)
	}
}

func resolveUserPassword(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	spec *api.KafkaSASLSpec,
) (string, string, error) {
	username, err := value(ctx, reader, namespace, spec.Username)
	if err != nil {
		return "", "", fmt.Errorf("resolve Kafka SASL username: %w", err)
	}
	password, err := value(ctx, reader, namespace, spec.Password)
	if err != nil {
		return "", "", fmt.Errorf("resolve Kafka SASL password: %w", err)
	}
	return username, password, nil
}

func resolveOAuth(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	spec *api.KafkaOAuthSpec,
) (sasl.Mechanism, error) {
	clientID, err := value(ctx, reader, namespace, &spec.ClientID)
	if err != nil {
		return nil, fmt.Errorf("resolve Kafka OAuth client ID: %w", err)
	}
	clientSecret, err := value(ctx, reader, namespace, &spec.ClientSecret)
	if err != nil {
		return nil, fmt.Errorf("resolve Kafka OAuth client secret: %w", err)
	}
	tokenSource := (&clientcredentials.Config{
		ClientID: clientID, ClientSecret: clientSecret, TokenURL: spec.TokenURL, Scopes: spec.Scopes,
	}).TokenSource(ctx)
	return oauth.Oauth(func(context.Context) (oauth.Auth, error) {
		token, err := tokenSource.Token()
		if err != nil {
			return oauth.Auth{}, err
		}
		return oauth.Auth{Token: token.AccessToken}, nil
	}), nil
}

func resolveKerberos(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	spec *api.KafkaKerberosSpec,
) (sasl.Mechanism, error) {
	username, err := value(ctx, reader, namespace, &spec.Username)
	if err != nil {
		return nil, fmt.Errorf("resolve Kafka Kerberos username: %w", err)
	}
	configBytes, err := secretKey(ctx, reader, namespace, spec.Config)
	if err != nil {
		return nil, fmt.Errorf("resolve Kafka Kerberos config: %w", err)
	}
	config, err := krbconfig.NewFromString(string(configBytes))
	if err != nil {
		return nil, fmt.Errorf("parse Kafka Kerberos config: %w", err)
	}
	var kerberosClient *krbclient.Client
	switch {
	case spec.Password != nil:
		password, err := value(ctx, reader, namespace, spec.Password)
		if err != nil {
			return nil, fmt.Errorf("resolve Kafka Kerberos password: %w", err)
		}
		kerberosClient = krbclient.NewWithPassword(username, spec.Realm, password, config)
	case spec.Keytab != nil:
		keytabBytes, err := secretKey(ctx, reader, namespace, spec.Keytab)
		if err != nil {
			return nil, fmt.Errorf("resolve Kafka Kerberos keytab: %w", err)
		}
		kt := new(keytab.Keytab)
		if err := kt.Unmarshal(keytabBytes); err != nil {
			return nil, fmt.Errorf("parse Kafka Kerberos keytab: %w", err)
		}
		kerberosClient = krbclient.NewWithKeytab(username, spec.Realm, kt, config)
	default:
		return nil, errors.New("Kafka Kerberos requires a password or keytab")
	}
	return kerberos.Auth{Client: kerberosClient, Service: spec.ServiceName}.AsMechanismWithClose(), nil
}

func resolveAWS(ctx context.Context, spec *api.KafkaAWSSpec) (sasl.Mechanism, error) {
	config, err := awssdk.LoadDefaultConfig(ctx, awssdk.WithRegion(spec.Region))
	if err != nil {
		return nil, fmt.Errorf("load AWS credentials for Kafka: %w", err)
	}
	return aws.ManagedStreamingIAM(func(ctx context.Context) (aws.Auth, error) {
		credentials, err := config.Credentials.Retrieve(ctx)
		if err != nil {
			return aws.Auth{}, err
		}
		return aws.Auth{
			AccessKey: credentials.AccessKeyID, SecretKey: credentials.SecretAccessKey,
			SessionToken: credentials.SessionToken,
		}, nil
	}), nil
}

func value(ctx context.Context, reader ctrlclient.Reader, namespace string, source *api.ValueSource) (string, error) {
	if source == nil {
		return "", errors.New("value source is missing")
	}
	if source.SecretKeyRef == nil {
		return source.Value, nil
	}
	bytes, err := secretKey(ctx, reader, namespace, source.SecretKeyRef)
	return string(bytes), err
}

func secretKey(
	ctx context.Context,
	reader ctrlclient.Reader,
	namespace string,
	selector *corev1.SecretKeySelector,
) ([]byte, error) {
	if selector == nil {
		return nil, errors.New("Secret key selector is missing")
	}
	secret := new(corev1.Secret)
	if err := reader.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: selector.Name}, secret); err != nil {
		return nil, err
	}
	data, ok := secret.Data[selector.Key]
	if !ok {
		return nil, fmt.Errorf("Secret %s/%s has no key %q", namespace, selector.Name, selector.Key)
	}
	return data, nil
}

module declcfg-inline-bundles

go 1.16

require (
	github.com/Shopify/logrus-bugsnag v0.0.0-20171204204709-577dee27f20d // indirect
	github.com/bshuster-repo/logrus-logstash-hook v1.0.0 // indirect
	github.com/operator-framework/operator-registry v1.16.1
	github.com/sirupsen/logrus v1.6.0
	github.com/spf13/cobra v1.1.3
	k8s.io/client-go v0.20.0
	rsc.io/letsencrypt v0.0.3 // indirect
)

replace github.com/operator-framework/operator-registry => ../../operator-framework/operator-registry

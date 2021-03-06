module github.com/joelanford/declcfg-inline-bundles

go 1.16

require (
	github.com/Shopify/logrus-bugsnag v0.0.0-20171204204709-577dee27f20d // indirect
	github.com/bshuster-repo/logrus-logstash-hook v1.0.0 // indirect
	github.com/h2non/filetype v1.1.1
	github.com/h2non/go-is-svg v0.0.0-20160927212452-35e8c4b0612c
	github.com/joelanford/ignore v0.0.0-20210610194209-63d4919d8fb2
	github.com/operator-framework/api v0.7.1
	github.com/operator-framework/operator-registry v1.17.4
	github.com/sirupsen/logrus v1.6.0
	github.com/spf13/cobra v1.1.3
	github.com/stretchr/testify v1.6.1
	golang.org/x/sync v0.0.0-20201020160332-67f06af15bc9
	k8s.io/apimachinery v0.20.6
	k8s.io/client-go v0.20.6
	rsc.io/letsencrypt v0.0.3 // indirect
	sigs.k8s.io/yaml v1.2.0
)

replace github.com/operator-framework/operator-registry => github.com/joelanford/operator-registry v1.12.2-0.20210721025046-a8d185d7a62c

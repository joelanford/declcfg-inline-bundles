package main

import (
	"context"
	"fmt"
	"github.com/operator-framework/operator-registry/pkg/declcfg"
	"github.com/operator-framework/operator-registry/pkg/image"
	"github.com/operator-framework/operator-registry/pkg/image/containerdregistry"
	"github.com/operator-framework/operator-registry/pkg/property"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"io/fs"
	"io/ioutil"
	"k8s.io/client-go/util/retry"
	"os"
	"path/filepath"
)

func main() {
	cmd := newCmd()
	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func newCmd() *cobra.Command {
	i := inliner{}
	cmd := &cobra.Command{
		Use: "declcfg-inline-bundles <configsDir> <bundleImage1> <bundleImage2> ... <bundleImageN>",
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			configsDir := args[0]
			i.bundleImages = args[1:]

			reg, err := containerdregistry.NewRegistry(containerdregistry.WithLog(noopLogger()))
			if err != nil {
				log.Fatalf("Could not create new containerd registry: %v")
			}
			defer func() {
				if err := reg.Destroy(); err != nil {
					log.Warnf("Could not destroy containerd registry: %v", err)
				}
			}()
			i.imageRegistry = reg

			i.cfg, err = declcfg.LoadDir(configsDir)
			if err != nil {
				log.Fatalf("Error loading declarative configuration directory: %v", err)
			}

			if len(i.bundleImages) == 0 {
				for _, b := range i.cfg.Bundles {
					i.bundleImages = append(i.bundleImages, b.Image)
				}
			}

			if err := i.InlineBundles(cmd.Context()); err != nil {
				log.Fatalf("Error inlining bundles: %v", err)
			}

			configsDirBak := fmt.Sprintf("%s.bak", configsDir)
			if err := os.Rename(configsDir, configsDirBak); err != nil {
				log.Fatalf("Error backing up existing configs directory: %v", err)
			}
			if err := os.RemoveAll(configsDir); err != nil {
				log.Fatalf("Error removing stale configs directory: %v", err)
			}
			if err := declcfg.WriteDir(*i.cfg, configsDir); err != nil {
				if err := os.Rename(configsDirBak, configsDir); err != nil {
					log.Fatalf("Error restoring configs directory backup from %q: %v", configsDirBak, err)
				}
				log.Fatalf("Error writing new configs directory: %v", err)
			}
			if err := os.RemoveAll(configsDirBak); err != nil {
				log.Fatalf("Error removing backup configs directory: %v", err)
			}
		},
	}
	cmd.Flags().BoolVar(&i.deleteNonHeadObjects, "delete-non-head-objects", false, "Delete objects for bundles that are not channel heads.")
	return cmd
}

func noopLogger() *log.Entry {
	l := log.New()
	l.Out = ioutil.Discard
	return log.NewEntry(l)
}

type inliner struct {
	cfg *declcfg.DeclarativeConfig
	bundleImages []string
	deleteNonHeadObjects bool

	imageRegistry image.Registry
}

func (i *inliner) InlineBundles(ctx context.Context) error {
	nonChannelHeads, err := i.getAllNonChannelHeads()
	if err != nil {
		return fmt.Errorf("get non-channel-head bundles: %v", err)
	}

	for _, bi := range i.bundleImages {
		var declBundle *declcfg.Bundle
		for idx := range i.cfg.Bundles {
			if i.cfg.Bundles[idx].Image == bi {
				declBundle = &i.cfg.Bundles[idx]
				break
			}
		}
		if declBundle == nil {
			log.Warnf("Skipping bundle image %q: not found in index", bi)
			continue
		}

		if _, ok := nonChannelHeads[declBundle.Image]; ok && i.deleteNonHeadObjects {
			log.Warnf("Skipping bundle image %q: not a channel head", bi)
			continue
		}

		ref := image.SimpleReference(bi)
		if err := i.PopulateBundleObjects(ctx, declBundle, ref); err != nil {
			return fmt.Errorf("populate objects for bundle %q: %v", declBundle.Name, err)
		}
	}

	if i.deleteNonHeadObjects {
		for idx := range i.cfg.Bundles {
			b := &i.cfg.Bundles[idx]
			if _, ok := nonChannelHeads[b.Image]; !ok {
				continue
			}
			if deleteBundleObjects(b) {
				log.Infof("Deleted objects for non-channel-head bundle %q", b.Name)
			}
		}
	}

	return nil
}

func (i inliner) PopulateBundleObjects(ctx context.Context, b *declcfg.Bundle, ref image.Reference) error {
	log.Infof("Pulling bundle image %q", ref)
	if err := retry.OnError(retry.DefaultRetry,
		func(err error) bool {
			log.Warnf("    Error pulling image: %v. Retrying.", err)
			return true
		},
		func() error { return i.imageRegistry.Pull(ctx, ref)}); err != nil {
		return  fmt.Errorf("pull image %q: %v", ref, err)
	}

	lbls, err := i.imageRegistry.Labels(ctx, ref)
	if err != nil {
		return  fmt.Errorf("get labels for bundle %q: %v", ref, err)
	}

	tempDir, err := ioutil.TempDir("", ".tmp.declcfg-inline-bundles-")
	if err != nil {
		return  fmt.Errorf("create temp directory: %v", err)
	}
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			log.Warnf("delete temp directory %q: %v", tempDir, err)
		}
	}()

	if err := i.imageRegistry.Unpack(ctx, ref, tempDir); err != nil {
		return  fmt.Errorf("unpacked bundle %q: %v", ref, err)
	}

	manifestDir, ok := lbls["operators.operatorframework.io.bundle.manifests.v1"]
	if !ok {
		manifestDir = "manifests/"
	}
	manifestDir = filepath.Join(tempDir, manifestDir)


	// Clear out existing bundle objects and object properties
	deleteBundleObjects(b)

	if err := filepath.WalkDir(manifestDir, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		obj, err := ioutil.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read file %q: %v", path, err)
		}
		ref := filepath.Join("objects", b.Name, info.Name())
		b.Properties = append(b.Properties, property.MustBuildBundleObjectRef(ref))
		b.Objects = append(b.Objects, string(obj))
		return nil
	}); err != nil {
		return fmt.Errorf("collect objects for bundle %q: %v", ref, err)
	}
	return nil
}

func (i inliner) getAllNonChannelHeads() (map[string]struct{}, error) {
	m, err := declcfg.ConvertToModel(*i.cfg)
	if err != nil {
		return nil, fmt.Errorf("convert index to model: %v", err)
	}

	nonChannelHeads := map[string]struct{}{}
	for _, pkg := range m {
		for _, ch := range pkg.Channels {
			for _, b := range ch.Bundles {
				nonChannelHeads[b.Image] = struct{}{}
			}
		}
	}
	for _, pkg := range m {
		for _, ch := range pkg.Channels {
			head, _ := ch.Head()
			delete(nonChannelHeads, head.Image)
		}
	}

	return nonChannelHeads, nil
}

func deleteBundleObjects(b *declcfg.Bundle) bool {
	deleted := false

	b.CsvJSON = ""
	if len(b.Objects) > 0 {
		b.Objects = nil
		deleted = true
	}

	temp := b.Properties[:0]
	for _, p := range b.Properties {
		if p.Type == property.TypeBundleObject {
			deleted = true
		} else {
			temp = append(temp, p)
		}
	}
	b.Properties = temp
	return deleted
}

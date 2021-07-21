package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"

	"github.com/operator-framework/operator-registry/pkg/image"
	"github.com/operator-framework/operator-registry/pkg/image/containerdregistry"
	"github.com/operator-framework/operator-registry/pkg/registry"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/retry"

	"github.com/operator-framework/operator-registry/alpha/declcfg"
	"github.com/operator-framework/operator-registry/alpha/property"
)

var nonRetryableRegex = regexp.MustCompile(`(error resolving name)`)

func main() {
	cmd := newCmd()
	if err := cmd.Execute(); err != nil {
		log.Fatal(err)
	}
}

func newCmd() *cobra.Command {
	var pruneNonHeadObjects bool
	cmd := &cobra.Command{
		Use:  "declcfg-inline-bundles <configsDir> <bundleImage1> <bundleImage2> ... <bundleImageN>",
		Args: cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			rootDir := args[0]
			root := os.DirFS(rootDir)
			bundleImages := sets.NewString(args[1:]...)

			imageRegistry, err := containerdregistry.NewRegistry(containerdregistry.WithLog(noopLogger()))
			if err != nil {
				log.Fatalf("Could not create new containerd registry: %v")
			}
			defer func() {
				if err := imageRegistry.Destroy(); err != nil {
					log.Warnf("Could not destroy containerd registry: %v", err)
				}
			}()

			eg := errgroup.Group{}

			log.Info("Loading declarative configuration directory")
			cfg, err := declcfg.LoadFS(root)
			if err != nil {
				log.Fatal(err)
			}

			allBundleImages := sets.NewString()
			for _, b := range cfg.Bundles {
				allBundleImages.Insert(b.Image)
			}
			notPresentImages := bundleImages.Difference(allBundleImages)
			if notPresentImages.Len() > 0 {
				log.Fatalf("requested images not found: %v", notPresentImages.List())
			}

			nonChannelHeads := sets.NewString()
			if pruneNonHeadObjects {
				nonChannelHeads, err = getAllNonChannelHeads(*cfg)
				if err != nil {
					log.Fatal(err)
				}
			}

			declcfg.WalkFS(root, func(path string, fcfg *declcfg.DeclarativeConfig, err error) error {
				if err != nil {
					return err
				}
				plog := log.New().WithField("path", filepath.Join(rootDir, path))
				eg.Go(func() error {
					for i, b := range fcfg.Bundles {
						blog := plog.WithField("image", b.Image)
						// prune bundle objects from all non-channel heads, if pruning is enabled
						if pruneNonHeadObjects && nonChannelHeads.Has(b.Image) {
							props := b.Properties[:0]
							for _, p := range b.Properties {
								if p.Type != property.TypeBundleObject {
									props = append(props, p)
								}
							}
							if len(props) != len(fcfg.Bundles[i].Properties) {
								blog.Info("pruned olm.bundle.object properties")
							}
							fcfg.Bundles[i].Properties = props
						}
						if pruneNonHeadObjects && nonChannelHeads.Has(b.Image) {
							blog.Info("skipping non-channel head")
						} else if bundleImages.Len() == 0 || bundleImages.Has(b.Image) {
							imgRef := image.SimpleReference(b.Image)

							if err := retry.OnError(retry.DefaultRetry,
								func(err error) bool {
									if nonRetryableRegex.MatchString(err.Error()) {
										return false
									}
									log.Warnf("  Error pulling image: %v. Retrying.", err)
									return true
								},
								func() error { return imageRegistry.Pull(cmd.Context(), imgRef) }); err != nil {
								return fmt.Errorf("pull image %q: %v", imgRef, err)
							}

							tmpDir, err := os.MkdirTemp("", "declcfg-inline-bundles-")
							if err != nil {
								return err
							}
							if err := imageRegistry.Unpack(cmd.Context(), imgRef, tmpDir); err != nil {
								return err
							}
							ii, err := registry.NewImageInput(image.SimpleReference(b.Image), tmpDir)
							if err != nil {
								return err
							}
							props := b.Properties[:0]
							for _, p := range b.Properties {
								if p.Type != property.TypeBundleObject {
									props = append(props, p)
								} else {
									var obj property.BundleObject
									if err := json.Unmarshal(p.Value, &obj); err != nil {
										return err
									}
									// Delete the referenced file if the object property is a reference.
									if obj.IsRef() {
										os.RemoveAll(filepath.Join(rootDir, filepath.Dir(path), obj.GetRef()))
									}
								}
							}

							for _, obj := range ii.Bundle.Objects {
								objJson, err := json.Marshal(obj)
								if err != nil {
									return err
								}
								props = append(props, property.MustBuildBundleObjectData(objJson))
							}
							b.Properties = props
							fcfg.Bundles[i] = b
							blog.Info("inlined olm.bundle.object properties")
						}
					}
					f, err := os.OpenFile(filepath.Join(rootDir, path), os.O_RDWR|os.O_TRUNC, 0666)
					if err != nil {
						return err
					}
					if filepath.Ext(path) == ".yaml" {
						if err := declcfg.WriteYAML(*fcfg, f); err != nil {
							return err
						}
					} else {
						if err := declcfg.WriteJSON(*fcfg, f); err != nil {
							return err
						}
					}
					return nil
				})
				return nil
			})

			if err := eg.Wait(); err != nil {
				log.Fatal(err)
			}
		},
	}
	cmd.Flags().BoolVarP(&pruneNonHeadObjects, "prune-non-head-objects", "p", false, "Prune objects for bundles that are not channel heads.")
	return cmd
}

func noopLogger() *log.Entry {
	l := log.New()
	l.Out = ioutil.Discard
	return log.NewEntry(l)
}

func getAllNonChannelHeads(cfg declcfg.DeclarativeConfig) (sets.String, error) {
	m, err := declcfg.ConvertToModel(cfg)
	if err != nil {
		return nil, fmt.Errorf("convert index to model: %v", err)
	}

	nonChannelHeads := sets.NewString()
	for _, pkg := range m {
		for _, ch := range pkg.Channels {
			for _, b := range ch.Bundles {
				nonChannelHeads.Insert(b.Image)
			}
		}
	}
	for _, pkg := range m {
		for _, ch := range pkg.Channels {
			head, err := ch.Head()
			if err != nil {
				return nil, err
			}
			nonChannelHeads.Delete(head.Image)
		}
	}
	return nonChannelHeads, nil
}

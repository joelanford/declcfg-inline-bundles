package model

import (
	"errors"
	"fmt"
	"strings"

	"github.com/h2non/filetype"
	"github.com/h2non/filetype/matchers"
	"github.com/h2non/filetype/types"
	svg "github.com/h2non/go-is-svg"

	"github.com/operator-framework/operator-registry/pkg/property"
)

func init() {
	t := types.NewType("svg", "image/svg+xml")
	filetype.AddMatcher(t, svg.Is)
	matchers.Image[types.NewType("svg", "image/svg+xml")] = svg.Is
}

type Model map[string]*Package

func (m Model) Validate() error {
	for name, pkg := range m {
		if name != pkg.Name {
			return fmt.Errorf("package key %q does not match package name %q", name, pkg.Name)
		}
		if err := pkg.Validate(); err != nil {
			return fmt.Errorf("invalid package %q: %v", pkg.Name, err)
		}
	}
	return nil
}

type Package struct {
	Name           string
	Description    string
	Icon           *Icon
	DefaultChannel *Channel
	Channels       map[string]*Channel
}

func (m *Package) Validate() error {
	if m.Name == "" {
		return errors.New("package name must not be empty")
	}

	if err := m.Icon.Validate(); err != nil {
		return fmt.Errorf("invalid icon: %v", err)
	}

	if len(m.Channels) == 0 {
		return fmt.Errorf("package must contain at least one channel")
	}

	if m.DefaultChannel == nil {
		return fmt.Errorf("default channel must be set")
	}

	foundDefault := false
	for name, ch := range m.Channels {
		if name != ch.Name {
			return fmt.Errorf("channel key %q does not match channel name %q", name, ch.Name)
		}
		if err := ch.Validate(); err != nil {
			return fmt.Errorf("invalid channel %q: %v", ch.Name, err)
		}
		if ch == m.DefaultChannel {
			foundDefault = true
		}
		if ch.Package != m {
			return fmt.Errorf("channel %q not correctly linked to parent package", ch.Name)
		}
	}

	if !foundDefault {
		return fmt.Errorf("default channel %q not found in channels list", m.DefaultChannel.Name)
	}
	return nil
}

type Icon struct {
	Data      []byte
	MediaType string
}

func (i *Icon) Validate() error {
	if i == nil {
		return nil
	}
	// TODO(joelanford): Should we check that data and mediatype are set,
	//   and detect the media type of the data and compare it to the
	//   mediatype listed in the icon field? Currently, some production
	//   index databases are failing these tests, so leaving this
	//   commented out for now.
	//if len(i.Data) == 0 {
	//	return errors.New("icon data must be set if icon is defined")
	//}
	//if len(i.MediaType) == 0 {
	//	return errors.New("icon mediatype must be set if icon is defined")
	//}
	//return i.validateData()
	return nil
}

func (i *Icon) validateData() error {
	if !filetype.IsImage(i.Data) {
		return errors.New("icon data is not an image")
	}
	t, err := filetype.Match(i.Data)
	if err != nil {
		return err
	}
	if t.MIME.Value != i.MediaType {
		return fmt.Errorf("icon media type %q does not match detected media type %q", i.MediaType, t.MIME.Value)
	}
	return nil
}

type Channel struct {
	Package *Package
	Name    string
	Bundles map[string]*Bundle
}

// TODO(joelanford): This function determines the channel head by finding the bundle that has 0
//   incoming edges, based on replaces and skips. It also expects to find exactly one such bundle.
//   Is this the correct algorithm?
func (c Channel) Head() (*Bundle, error) {
	incoming := map[string]int{}
	for _, b := range c.Bundles {
		if b.Replaces != "" {
			incoming[b.Replaces]++
		}
		for _, skip := range b.Skips {
			incoming[skip]++
		}
	}
	var heads []*Bundle
	for _, b := range c.Bundles {
		if _, ok := incoming[b.Name]; !ok {
			heads = append(heads, b)
		}
	}
	if len(heads) == 0 {
		return nil, fmt.Errorf("no channel head found in graph")
	}
	if len(heads) > 1 {
		var headNames []string
		for _, head := range heads {
			headNames = append(headNames, head.Name)
		}
		return nil, fmt.Errorf("multiple channel heads found in graph: %s", strings.Join(headNames, ", "))
	}
	return heads[0], nil
}

func (c *Channel) Validate() error {
	if c.Name == "" {
		return errors.New("channel name must not be empty")
	}

	if c.Package == nil {
		return errors.New("package must be set")
	}

	if len(c.Bundles) == 0 {
		return fmt.Errorf("channel must contain at least one bundle")
	}

	if _, err := c.Head(); err != nil {
		return err
	}

	for name, b := range c.Bundles {
		if name != b.Name {
			return fmt.Errorf("bundle key %q does not match bundle name %q", name, b.Name)
		}
		if err := b.Validate(); err != nil {
			return fmt.Errorf("invalid bundle %q: %v", b.Name, err)
		}
		if b.Channel != c {
			return fmt.Errorf("bundle %q not correctly linked to parent channel", b.Name)
		}
	}
	return nil
}

type Bundle struct {
	Package       *Package
	Channel       *Channel
	Name          string
	Image         string
	Replaces      string
	Skips         []string
	Properties    []property.Property
	RelatedImages []RelatedImage

	// These fields are present so that we can continue serving
	// the GRPC API the way packageserver expects us to in a
	// backwards-compatible way.
	Objects []string
	CsvJSON string
}

func (b *Bundle) Validate() error {
	if b.Name == "" {
		return errors.New("name must be set")
	}
	if b.Channel == nil {
		return errors.New("channel must be set")
	}
	if b.Package == nil {
		return errors.New("package must be set")
	}
	if b.Package != b.Channel.Package {
		return errors.New("package does not match channel's package")
	}
	if b.Replaces != "" {
		if _, ok := b.Channel.Bundles[b.Replaces]; !ok {
			return fmt.Errorf("replaces %q not found in channel", b.Replaces)
		}
	}
	props, err := property.Parse(b.Properties)
	if err != nil {
		return err
	}
	for i, skip := range b.Skips {
		if skip == "" {
			return fmt.Errorf("skip[%d] is empty", i)
		}
	}
	// TODO(joelanford): Validate related images? It looks like some
	//   CSVs in production databases use incorrect fields ([name,value]
	//   instead of [name,image]), which results in empty image values.
	//   Example is in redhat-operators: 3scale-operator.v0.5.5
	//for i, relatedImage := range b.RelatedImages {
	//	if err := relatedImage.Validate(); err != nil {
	//		return fmt.Errorf("invalid related image[%d]: %v", i, err)
	//	}
	//}

	if err := property.ValidateBackCompat(*props); err != nil {
		return err
	}

	return nil
}

type RelatedImage struct {
	Name  string
	Image string
}

func (i RelatedImage) Validate() error {
	if i.Name == "" {
		return fmt.Errorf("name must be set")
	}
	if i.Image == "" {
		return fmt.Errorf("image must be set")
	}
	return nil
}

func (m Model) Normalize() {
	for _, pkg := range m {
		for _, ch := range pkg.Channels {
			for _, b := range ch.Bundles {
				for i := range b.Properties {
					// Ensure property value is encoded in a standard way.
					if normalized, err := property.Build(&b.Properties[i]); err == nil {
						b.Properties[i] = *normalized
					}
				}
			}
		}
	}
}

package packager

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/aws/aws-sdk-go/service/sts/stsiface"
	"github.com/glassechidna/stackit/pkg/stackit/cfnyaml"
	"github.com/glassechidna/stackit/pkg/zipper"
	"github.com/pkg/errors"
	"io"
	"path/filepath"
	"strings"
)

type Packager struct {
	s3     s3iface.S3API
	sts    stsiface.STSAPI
	region string

	cachedBucketName string
}

func New(s3 s3iface.S3API, sts stsiface.STSAPI, region string) *Packager {
	return &Packager{
		s3:     s3,
		sts:    sts,
		region: region,
	}
}

type TemplateReader interface {
	fmt.Stringer
	Name() string
}

func (p *Packager) Package(ctx context.Context, prefix string, templateReader TemplateReader, writer io.Writer) (*string, error) {
	c, err := cfnyaml.Parse([]byte(templateReader.String()))
	if err != nil {
		return nil, err
	}

	nodes, err := c.PackageableNodes()
	artifacts := map[string]string{}
	for _, n := range nodes {
		path := n.Value
		realPath := filepath.Join(filepath.Dir(templateReader.Name()), path)
		if err != nil {
			return nil, errors.Wrap(err, "determining artifact path relative to template")
		}

		artifacts[path], err = zipper.Zip(realPath)
		if err != nil {
			return nil, errors.Wrapf(err, "zipping `%s`", path)
		}
	}

	uploads := map[string]*UploadedObject{}
	errch := make(chan error)
	for artifactPath, zipPath := range artifacts {
		go func(artifactPath, zipPath string) {
			key := strings.TrimPrefix(fmt.Sprintf("%s/%s.zip", prefix, strings.TrimSuffix(filepath.Base(artifactPath), ".zip")), "/")
			up, err := p.Upload(ctx, key, zipPath)
			uploads[artifactPath] = up
			errch <- errors.Wrap(err, "uploading zip to s3")
			if up != nil {
				fmt.Fprintf(writer, "Uploaded %s to s3://%s/%s (v = %s)\n", artifactPath, up.Bucket, up.Key, up.VersionId)
			}
		}(artifactPath, zipPath)
	}

	for range artifacts {
		err = <-errch
		if err != nil {
			return nil, err
		}
	}

	for _, n := range nodes {
		path := n.Value
		uploaded := uploads[path]
		n.Replace(uploaded.Bucket, uploaded.Key, uploaded.VersionId)
	}

	templateBody := c.String()
	return &templateBody, nil
}
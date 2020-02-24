// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package miniogw_test

import (
	"context"
	"errors"
	"flag"
	"os"
	"testing"
	"time"

	"github.com/minio/cli"
	minio "github.com/minio/minio/cmd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"

	"storj.io/common/identity"
	"storj.io/common/identity/testidentity"
	"storj.io/common/testcontext"
	"storj.io/gateway/miniogw"
	"storj.io/storj/cmd/uplink/cmd"
	"storj.io/storj/private/s3client"
	"storj.io/storj/private/testplanet"
	"storj.io/storj/satellite/console"
	"storj.io/uplink"
)

type config struct {
	Server miniogw.ServerConfig
	Minio  miniogw.MinioConfig
}

func TestUploadDownload(t *testing.T) {
	testplanet.Run(t, testplanet.Config{
		SatelliteCount: 1, StorageNodeCount: 4, UplinkCount: 1,
	}, func(t *testing.T, ctx *testcontext.Context, planet *testplanet.Planet) {
		// add project to satisfy constraint
		_, err := planet.Satellites[0].DB.Console().Projects().Insert(ctx, &console.Project{
			Name: "testProject",
		})
		require.NoError(t, err)

		var gwCfg config
		gwCfg.Minio.Dir = ctx.Dir("minio")
		gwCfg.Server.Address = "127.0.0.1:7777"

		uplinkCfg := planet.Uplinks[0].GetConfig(planet.Satellites[0])

		planet.Start(ctx)

		// create identity for gateway
		ca, err := testidentity.NewTestCA(ctx)
		assert.NoError(t, err)
		identity, err := ca.NewIdentity()
		assert.NoError(t, err)

		// setup and start gateway
		go func() {
			// TODO: this leaks the gateway server, however it shouldn't
			err := runGateway(ctx, gwCfg, uplinkCfg, zaptest.NewLogger(t), identity)
			if err != nil {
				t.Log(err)
			}
		}()

		time.Sleep(100 * time.Millisecond)

		client, err := s3client.NewMinio(s3client.Config{
			S3Gateway:     gwCfg.Server.Address,
			Satellite:     planet.Satellites[0].Addr(),
			AccessKey:     gwCfg.Minio.AccessKey,
			SecretKey:     gwCfg.Minio.SecretKey,
			APIKey:        uplinkCfg.Legacy.Client.APIKey,
			EncryptionKey: "fake-encryption-key",
			NoSSL:         true,
		})
		assert.NoError(t, err)

		bucket := "bucket"

		err = client.MakeBucket(bucket, "")
		assert.NoError(t, err)

		// generate enough data for a remote segment
		data := []byte{}
		for i := 0; i < 5000; i++ {
			data = append(data, 'a')
		}

		objectName := "testdata"

		err = client.Upload(bucket, objectName, data)
		assert.NoError(t, err)

		buffer := make([]byte, len(data))

		bytes, err := client.Download(bucket, objectName, buffer)
		assert.NoError(t, err)

		assert.Equal(t, string(data), string(bytes))
	})
}

// runGateway creates and starts a gateway
func runGateway(ctx context.Context, gwCfg config, uplinkCfg cmd.Config, log *zap.Logger, ident *identity.FullIdentity) (err error) {

	// set gateway flags
	flags := flag.NewFlagSet("gateway", flag.ExitOnError)
	flags.String("address", gwCfg.Server.Address, "")
	flags.String("config-dir", gwCfg.Minio.Dir, "")
	flags.Bool("quiet", true, "")

	// create *cli.Context with gateway flags
	cliCtx := cli.NewContext(cli.NewApp(), flags, nil)

	// TODO: setting the flag on flagset and cliCtx seems redundant, but output is not quiet otherwise
	err = cliCtx.Set("quiet", "true")
	if err != nil {
		return err
	}

	err = os.Setenv("MINIO_ACCESS_KEY", gwCfg.Minio.AccessKey)
	if err != nil {
		return err
	}

	err = os.Setenv("MINIO_SECRET_KEY", gwCfg.Minio.SecretKey)
	if err != nil {
		return err
	}

	oldAccess, err := uplinkCfg.GetAccess()
	if err != nil {
		return err
	}

	serializedAccess, err := oldAccess.Serialize()
	if err != nil {
		return err
	}

	access, err := uplink.ParseAccess(serializedAccess)
	if err != nil {
		return err
	}

	project, err := uplink.OpenProject(ctx, access)
	if err != nil {
		return err
	}

	gw := miniogw.NewStorjGateway(project)

	minio.StartGateway(cliCtx, miniogw.Logging(gw, log))
	return errors.New("unexpected minio exit")
}

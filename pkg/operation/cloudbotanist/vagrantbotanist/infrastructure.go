// Copyright 2018 The Gardener Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vagrantbotanist

import (
	"fmt"

	"path/filepath"

	"github.com/gardener/gardener/pkg/client/vagrant"
	"github.com/gardener/gardener/pkg/operation/common"
	pb "github.com/gardener/gardener/pkg/vagrantprovider"
	"golang.org/x/net/context"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeployInfrastructure talks to the gardener-vagrant-provider which creates the nodes.
func (b *VagrantBotanist) DeployInfrastructure() error {

	// TODO: use b.Operation.ComputeDownloaderCloudConfig("vagrant")
	// At this stage we don't have the shoot api server
	chart, err := b.Operation.ChartSeedRenderer.Render(filepath.Join(common.ChartPath, "shoot-cloud-config", "charts", "downloader"), "shoot-cloud-config-downloader", metav1.NamespaceSystem, map[string]interface{}{
		"kubeconfig": string(b.Operation.Secrets["cloud-config-downloader"].Data["kubeconfig"]),
		"secretName": b.Operation.Shoot.ComputeCloudConfigSecretName("vagrant"),
	})
	if err != nil {
		return err
	}

	var cloudConfig = ""
	for fileName, chartFile := range chart.Files {
		if fileName == "downloader/templates/cloud-config.yaml" {
			cloudConfig = chartFile
		}
	}

	client, conn, err := vagrant.New(fmt.Sprintf(b.Shoot.Info.Spec.Cloud.Vagrant.Endpoint))
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = client.Start(context.Background(), &pb.StartRequest{
		Cloudconfig: cloudConfig,
		Id:          1,
	})

	return err
}

// DestroyInfrastructure talks to the gardener-vagrant-provider which destroys the nodes.
func (b *VagrantBotanist) DestroyInfrastructure() error {
	client, conn, err := vagrant.New(fmt.Sprintf(b.Shoot.Info.Spec.Cloud.Vagrant.Endpoint))
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = client.Delete(context.Background(), &pb.DeleteRequest{
		Id: 1,
	})
	return err
}

// DeployBackupInfrastructure kicks off a Terraform job which creates the infrastructure resources for backup.
func (b *VagrantBotanist) DeployBackupInfrastructure() error {
	return nil
}

// DestroyBackupInfrastructure kicks off a Terraform job which destroys the infrastructure for backup.
func (b *VagrantBotanist) DestroyBackupInfrastructure() error {
	return nil
}

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// newS3Client creates an S3 client from StorageInfo and credential data.
func newS3Client(ctx context.Context, typedClient kubernetes.Interface, namespace string, storage *StorageInfo) (*s3.Client, error) {
	// Read credential secret
	secret, err := typedClient.CoreV1().Secrets(namespace).Get(ctx, storage.CredentialName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("reading credential secret %s: %w", storage.CredentialName, err)
	}

	credData, ok := secret.Data[storage.CredentialKey]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s", storage.CredentialKey, storage.CredentialName)
	}

	accessKey, secretKey, err := parseAWSCredentials(string(credData))
	if err != nil {
		return nil, fmt.Errorf("parsing AWS credentials: %w", err)
	}

	region := storage.Region
	if region == "" {
		region = "us-east-1"
	}

	opts := func(o *s3.Options) {
		o.Region = region
		o.Credentials = credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")
		if storage.S3URL != "" {
			o.BaseEndpoint = aws.String(storage.S3URL)
		}
		if storage.S3ForcePathStyle {
			o.UsePathStyle = true
		}
	}

	client := s3.New(s3.Options{}, opts)
	return client, nil
}

// parseAWSCredentials parses AWS shared credentials format:
//
//	[default]
//	aws_access_key_id = AKIA...
//	aws_secret_access_key = secret...
func parseAWSCredentials(data string) (accessKey, secretKey string, err error) {
	scanner := bufio.NewScanner(strings.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "aws_access_key_id":
			accessKey = value
		case "aws_secret_access_key":
			secretKey = value
		}
	}

	if accessKey == "" || secretKey == "" {
		return "", "", fmt.Errorf("incomplete credentials: access_key=%v, secret_key=%v",
			accessKey != "", secretKey != "")
	}
	return accessKey, secretKey, nil
}

// discoverVMs extracts unique VM identities from DataUploads.
func discoverVMs(dataUploads []DataUploadInfo) []VMIdentity {
	seen := make(map[VMIdentity]bool)
	var vms []VMIdentity
	for _, du := range dataUploads {
		if du.VMName == "" || du.VMNamespace == "" {
			continue
		}
		id := VMIdentity{Namespace: du.VMNamespace, Name: du.VMName}
		if !seen[id] {
			seen[id] = true
			vms = append(vms, id)
		}
	}
	return vms
}

// fetchChains fetches index.json for all discovered VMs from S3.
func fetchChains(ctx context.Context, client *s3.Client, storage *StorageInfo, vms []VMIdentity) (map[VMIdentity]*VMIndex, string) {
	chains := make(map[VMIdentity]*VMIndex)
	var lastErr string

	for _, vm := range vms {
		idx, err := fetchVMIndex(ctx, client, storage, vm)
		if err != nil {
			lastErr = fmt.Sprintf("VM %s/%s: %v", vm.Namespace, vm.Name, err)
			continue
		}
		chains[vm] = idx
	}

	return chains, lastErr
}

// fetchVMIndex fetches and parses a single VM's index.json from S3.
func fetchVMIndex(ctx context.Context, client *s3.Client, storage *StorageInfo, vm VMIdentity) (*VMIndex, error) {
	// Build the S3 key: <prefix>-kubevirt-datamover/checkpoints/<ns>/<vm>/index.json
	prefix := storage.Prefix
	if prefix == "" {
		prefix = "velero"
	}
	key := fmt.Sprintf("%s-kubevirt-datamover/checkpoints/%s/%s/index.json",
		prefix, vm.Namespace, vm.Name)

	input := &s3.GetObjectInput{
		Bucket: aws.String(storage.Bucket),
		Key:    aws.String(key),
	}

	result, err := client.GetObject(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("GetObject %s: %w", key, err)
	}
	defer result.Body.Close()

	// Limit read to 10 MiB to prevent OOM on malformed objects
	const maxIndexSize = 10 << 20
	data, err := io.ReadAll(io.LimitReader(result.Body, maxIndexSize))
	if err != nil {
		return nil, fmt.Errorf("reading body: %w", err)
	}

	var idx VMIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing JSON: %w", err)
	}

	return &idx, nil
}

// checkpointTotalSize returns the sum of all file sizes in a checkpoint.
func checkpointTotalSize(cp CheckpointEntry) int64 {
	var total int64
	for _, f := range cp.Files {
		total += f.Size
	}
	return total
}

// vmIndexTotalSize returns the sum of all checkpoint sizes for a VM index.
func vmIndexTotalSize(idx *VMIndex) int64 {
	var total int64
	for _, cp := range idx.Checkpoints {
		total += checkpointTotalSize(cp)
	}
	return total
}

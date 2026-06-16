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

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/migtools/kubevirt-datamover-monitor/internal/monitor"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	var (
		namespace     string
		kubeconfig    string
		interval      time.Duration
		chainInterval time.Duration
		logLines      int64
		noS3          bool
		debugFile     string
		reportFile    string
		showVersion   bool
	)

	defaultKubeconfig := ""
	if home := homedir.HomeDir(); home != "" {
		defaultKubeconfig = filepath.Join(home, ".kube", "config")
	}
	if envKC := os.Getenv("KUBECONFIG"); envKC != "" {
		defaultKubeconfig = envKC
	}

	flag.StringVar(&namespace, "namespace", "openshift-adp", "Namespace to watch")
	flag.StringVar(&namespace, "n", "openshift-adp", "Namespace to watch (shorthand)")
	flag.StringVar(&kubeconfig, "kubeconfig", defaultKubeconfig, "Path to kubeconfig")
	flag.DurationVar(&interval, "interval", 2*time.Second, "K8s polling interval")
	flag.DurationVar(&chainInterval, "chain-interval", 30*time.Second, "S3 chain refresh interval")
	flag.Int64Var(&logLines, "log-lines", 10, "Tail lines per pod")
	flag.BoolVar(&noS3, "no-s3", false, "Disable S3 chain fetch")
	flag.StringVar(&debugFile, "debug-file", "", "Write debug state snapshots to this file")
	flag.StringVar(&reportFile, "report-file", "datamover-report.md", "Write phase timing report to this file (empty to disable)")
	flag.BoolVar(&showVersion, "version", false, "Print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Println("kubevirt-datamover-monitor", Version)
		return
	}

	// Validate inputs
	if interval <= 0 {
		fmt.Fprintf(os.Stderr, "Error: --interval must be positive (got %s)\n", interval)
		os.Exit(1)
	}
	if chainInterval <= 0 {
		fmt.Fprintf(os.Stderr, "Error: --chain-interval must be positive (got %s)\n", chainInterval)
		os.Exit(1)
	}
	if logLines < 0 {
		fmt.Fprintf(os.Stderr, "Error: --log-lines must be non-negative (got %d)\n", logLines)
		os.Exit(1)
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building kubeconfig: %v\n", err)
		os.Exit(1)
	}

	dynClient, err := dynamic.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating dynamic client: %v\n", err)
		os.Exit(1)
	}

	typedClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating typed client: %v\n", err)
		os.Exit(1)
	}

	if err := monitor.Run(monitor.Config{
		Namespace:     namespace,
		Interval:      interval,
		ChainInterval: chainInterval,
		LogLines:      logLines,
		NoS3:          noS3,
		DebugFile:     debugFile,
		ReportFile:    reportFile,
	}, dynClient, typedClient); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

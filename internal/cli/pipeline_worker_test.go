package cli

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestPipelineWorkerPreservesConfigAndSSHFlags(t *testing.T) {
	oldConfig, oldSSH := cfgFile, sshHost
	t.Cleanup(func() { cfgFile, sshHost = oldConfig, oldSSH })
	cfgFile, sshHost = filepath.Join("relative project", "config.toml"), "operator@host"
	wantPath, err := filepath.Abs(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := pipelineWorkerFlags()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--config=" + wantPath, "--ssh=operator@host"}; !reflect.DeepEqual(flags, want) {
		t.Fatalf("worker flags = %#v, want %#v", flags, want)
	}
	cfgFile, sshHost = "", ""
	flags, err = pipelineWorkerFlags()
	if err != nil || len(flags) != 0 {
		t.Fatalf("default worker flags = %#v, %v", flags, err)
	}
}

func TestPipelineWorkerEndpointIsRegisteredAndHidden(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"__pipeline-worker"})
	if err != nil || cmd == nil || cmd.Name() != "__pipeline-worker" || !cmd.Hidden {
		t.Fatalf("worker endpoint unavailable or visible: %v %v", cmd, err)
	}
	if err := cmd.Args(cmd, []string{"only-one"}); err == nil {
		t.Fatal("worker accepted incomplete identity")
	}
}

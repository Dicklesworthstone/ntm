package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/ntm/internal/robot"
	"github.com/Dicklesworthstone/ntm/internal/swarm"
)

func runRobotContractSubprocess(t *testing.T, args ...string) (string, int) {
	t.Helper()
	encodedArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRobotProcessContractHelper$")
	cmd.Env = append(os.Environ(), "NTM_ROBOT_CONTRACT_ARGS="+string(encodedArgs))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("exec helper failed: %v (stderr=%s)", err, stderr.String())
		}
	}
	return stdout.String(), exitCode
}

func TestHealthBackpressureAndHeatmapCLI(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = prevJSON }()

	cmd := newHealthCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--backpressure", "--heatmap"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ntm health --backpressure --heatmap failed: %v", err)
	}
	text := buf.String()
	for _, token := range []string{
		"Backpressure Decision:",
		"Load-Shedding Contract [ntm.load_shedding.v1]:",
		"Operator Heatmap",
	} {
		if !strings.Contains(text, token) {
			t.Fatalf("expected %q in health backpressure output:\n%s", token, text)
		}
	}
}

func TestHealthBackpressureJSONAndInvalidSession(t *testing.T) {
	prevJSON := jsonOutput
	jsonOutput = true
	defer func() { jsonOutput = prevJSON }()

	cmd := newHealthCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--backpressure", "--session", "nonexistent_session_probe"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ntm health --backpressure --json failed: %v", err)
	}
	var out robot.BackpressureOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode health --backpressure --json output: %v\nraw=%s", err, buf.String())
	}
	if !out.Success || out.Session != "nonexistent_session_probe" {
		t.Fatalf("unexpected backpressure JSON envelope: %+v", out)
	}
	if out.LoadShedding.SchemaVersion != "ntm.load_shedding.v1" {
		t.Fatalf("load_shedding.schema_version = %q, want ntm.load_shedding.v1", out.LoadShedding.SchemaVersion)
	}

	jsonOutput = false
	badCmd := newHealthCmd()
	badCmd.SetOut(&bytes.Buffer{})
	badCmd.SetErr(&bytes.Buffer{})
	badCmd.SetArgs([]string{"alpha", "--backpressure", "--session", "beta"})
	if err := badCmd.Execute(); err == nil {
		t.Fatalf("expected error on conflicting positional session and --session flag")
	}
}

func TestSwarmLabSubcommandsCLI(t *testing.T) {
	prevJSON := jsonOutput
	defer func() { jsonOutput = prevJSON }()

	// 1. ntm swarm lab list (human text)
	jsonOutput = false
	swarmCmd := newSwarmCmd()
	var listBuf bytes.Buffer
	swarmCmd.SetOut(&listBuf)
	swarmCmd.SetErr(&listBuf)
	swarmCmd.SetArgs([]string{"lab", "list"})
	if err := swarmCmd.Execute(); err != nil {
		t.Fatalf("ntm swarm lab list failed: %v", err)
	}
	if !strings.Contains(listBuf.String(), "short_smoke") || !strings.Contains(listBuf.String(), "Overload Corpus Guide:") {
		t.Fatalf("unexpected swarm lab list output:\n%s", listBuf.String())
	}

	// 2. ntm swarm lab run --scenario short_smoke --artifact-dir ... --write-run ... --calibrate --json
	jsonOutput = true
	artifactDir := filepath.Join(t.TempDir(), "lab-artifacts")
	writeRunPath := filepath.Join(t.TempDir(), "raw-run.json")
	runCmd := newSwarmCmd()
	var runBuf bytes.Buffer
	runCmd.SetOut(&runBuf)
	runCmd.SetErr(&runBuf)
	runCmd.SetArgs([]string{
		"lab", "run",
		"--scenario", "short_smoke",
		"--artifact-dir", artifactDir,
		"--write-run", writeRunPath,
		"--calibrate",
		"--profile-id", "cli-test-profile",
	})
	if err := runCmd.Execute(); err != nil {
		t.Fatalf("ntm swarm lab run failed: %v", err)
	}
	var runOut robot.SwarmLabOutput
	if err := json.Unmarshal(runBuf.Bytes(), &runOut); err != nil {
		t.Fatalf("decode swarm lab run JSON: %v\nraw=%s", err, runBuf.String())
	}
	if !runOut.Success || len(runOut.Experiments) != 1 || runOut.Calibration == nil || runOut.WrittenRunPath != writeRunPath {
		t.Fatalf("unexpected swarm lab run JSON output: %+v", runOut)
	}
	if _, err := os.Stat(writeRunPath); err != nil {
		t.Fatalf("expected raw run artifact at %q: %v", writeRunPath, err)
	}
	if _, err := os.Stat(runOut.Experiments[0].ArtifactPaths.Summary); err != nil {
		t.Fatalf("expected experiment summary artifact: %v", err)
	}

	// 3. ntm swarm lab corpus --file ... --artifact-dir ... (human text)
	jsonOutput = false
	corpusBytes, err := json.MarshalIndent(swarm.BuiltInOverloadRegressionCorpus(), "", "  ")
	if err != nil {
		t.Fatalf("marshal builtin corpus: %v", err)
	}
	corpusPath := filepath.Join(t.TempDir(), "corpus.json")
	if err := os.WriteFile(corpusPath, corpusBytes, 0o644); err != nil {
		t.Fatalf("write corpus file: %v", err)
	}
	corpusCmd := newSwarmCmd()
	var corpusBuf bytes.Buffer
	corpusCmd.SetOut(&corpusBuf)
	corpusCmd.SetErr(&corpusBuf)
	corpusCmd.SetArgs([]string{
		"lab", "corpus",
		"--file", corpusPath,
		"--artifact-dir", filepath.Join(t.TempDir(), "corpus-out"),
	})
	if err := corpusCmd.Execute(); err != nil {
		t.Fatalf("ntm swarm lab corpus failed: %v", err)
	}
	if !strings.Contains(corpusBuf.String(), "Overload Regression Corpus [ntm.swarm.overload_regression.v1] success=true") {
		t.Fatalf("unexpected corpus CLI output:\n%s", corpusBuf.String())
	}

	// 4. ntm swarm lab calibrate --profile-id ... --include-live (human text)
	calCmd := newSwarmCmd()
	var calBuf bytes.Buffer
	calCmd.SetOut(&calBuf)
	calCmd.SetErr(&calBuf)
	calCmd.SetArgs([]string{"lab", "calibrate", "--profile-id", "cli-cal", "--include-live"})
	if err := calCmd.Execute(); err != nil {
		t.Fatalf("ntm swarm lab calibrate failed: %v", err)
	}
	if !strings.Contains(calBuf.String(), "Host Capacity Calibration [profile=cli-cal] success=true") {
		t.Fatalf("unexpected calibrate CLI output:\n%s", calBuf.String())
	}
}

func TestRootDispatcherBackpressureAndSwarmLab(t *testing.T) {
	// --robot-backpressure end-to-end
	bpRaw, bpCode := runRobotContractSubprocess(t, "--robot-backpressure", "--robot-format=json")
	if bpCode != 0 {
		t.Fatalf("--robot-backpressure exited with %d, raw=%s", bpCode, bpRaw)
	}
	var bpOut robot.BackpressureOutput
	if err := json.Unmarshal([]byte(bpRaw), &bpOut); err != nil {
		t.Fatalf("decode --robot-backpressure JSON: %v\nraw=%s", err, bpRaw)
	}
	if !bpOut.Success || bpOut.LoadShedding.SchemaVersion != "ntm.load_shedding.v1" {
		t.Fatalf("unexpected --robot-backpressure envelope: %+v", bpOut)
	}

	// --robot-swarm-lab --lab-action=run --lab-scenario=short_smoke
	labRaw, labCode := runRobotContractSubprocess(t, "--robot-swarm-lab", "--lab-action=run", "--lab-scenario=short_smoke", "--robot-format=json")
	if labCode != 0 {
		t.Fatalf("--robot-swarm-lab run exited with %d, raw=%s", labCode, labRaw)
	}
	var labOut robot.SwarmLabOutput
	if err := json.Unmarshal([]byte(labRaw), &labOut); err != nil {
		t.Fatalf("decode --robot-swarm-lab JSON: %v\nraw=%s", err, labRaw)
	}
	if !labOut.Success || labOut.Action != "run" || len(labOut.Experiments) != 1 {
		t.Fatalf("unexpected --robot-swarm-lab run envelope: %+v", labOut)
	}

	// --robot-swarm-lab --lab-action=corpus
	corpusRaw, corpusCode := runRobotContractSubprocess(t, "--robot-swarm-lab", "--lab-action=corpus", "--robot-format=json")
	if corpusCode != 0 {
		t.Fatalf("--robot-swarm-lab corpus exited with %d, raw=%s", corpusCode, corpusRaw)
	}
	var corpusOut robot.SwarmLabOutput
	if err := json.Unmarshal([]byte(corpusRaw), &corpusOut); err != nil {
		t.Fatalf("decode --robot-swarm-lab corpus JSON: %v\nraw=%s", err, corpusRaw)
	}
	if !corpusOut.Success || corpusOut.CorpusSummary == nil || !corpusOut.CorpusSummary.Success {
		t.Fatalf("unexpected --robot-swarm-lab corpus envelope: %+v", corpusOut)
	}
}

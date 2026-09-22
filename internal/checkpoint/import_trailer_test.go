package checkpoint

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestImportRejectsCorruptTrailerBeforeWriting(t *testing.T) {
	for _, verify := range []bool{false, true} {
		for _, overwrite := range []bool{false, true} {
			for _, corruption := range []string{"crc", "size", "truncated", "payload", "padding"} {
				t.Run(corruption+"/verify="+strconv.FormatBool(verify)+"/overwrite="+strconv.FormatBool(overwrite), func(t *testing.T) {
					storage := NewStorageWithDir(t.TempDir())
					cp := &Checkpoint{
						Version: CurrentVersion, ID: "20260922-120000-trailer", Name: "original",
						SessionName: "trailer-import", WorkingDir: t.TempDir(), CreatedAt: time.Now(),
						Session: SessionState{Panes: []PaneState{{ID: "%0", Index: 0}}}, PaneCount: 1,
					}
					if overwrite {
						if err := storage.Save(cp); err != nil {
							t.Fatal(err)
						}
					}
					cp.Name = "replacement"
					padding := make([]byte, 8192)
					if corruption == "payload" {
						padding = []byte("hidden archive data")
					} else if corruption == "padding" {
						padding = make([]byte, maxImportTarPadding+1)
					}
					encoded := checkpointTrailerArchive(t, cp, padding)
					switch corruption {
					case "crc":
						encoded[len(encoded)-8] ^= 1
					case "size":
						encoded[len(encoded)-4] ^= 1
					case "truncated":
						encoded = encoded[:len(encoded)-3]
					}
					archive := filepath.Join(t.TempDir(), "checkpoint.tar.gz")
					if err := os.WriteFile(archive, encoded, 0600); err != nil {
						t.Fatal(err)
					}
					result, err := storage.Import(archive, ImportOptions{VerifyChecksums: verify, AllowOverwrite: overwrite})
					if err == nil || result != nil {
						t.Fatalf("corrupt archive imported: %v, %v", result, err)
					}
					if overwrite {
						loaded, err := storage.Load(cp.SessionName, cp.ID)
						if err != nil || loaded.Name != "original" {
							t.Fatalf("failed import changed existing checkpoint: %v, %v", loaded, err)
						}
					} else {
						cpDir, err := storage.safeCheckpointDir(cp.SessionName, cp.ID)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := os.Stat(cpDir); !os.IsNotExist(err) {
							t.Fatalf("failed import created checkpoint: %v", err)
						}
					}
				})
			}
		}
	}
}

func checkpointTrailerArchive(t *testing.T, cp *Checkpoint, padding []byte) []byte {
	t.Helper()
	metadata, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	session, err := json.Marshal(cp.Session)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(&ExportManifest{
		Version: 1, SessionName: cp.SessionName, CheckpointID: cp.ID, CheckpointName: cp.Name,
		Checksums: map[string]string{MetadataFile: sha256sum(metadata), SessionFile: sha256sum(session)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	gw := gzip.NewWriter(&encoded)
	tw := tar.NewWriter(gw)
	for _, file := range []struct {
		name string
		data []byte
	}{{MetadataFile, metadata}, {SessionFile, session}, {"MANIFEST.json", manifest}} {
		if err := writeTarEntry(tw, file.name, file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Write(padding); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

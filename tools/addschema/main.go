// Tool addschema injects a config.schema.json at the root of an existing
// package tarball (zarf package create does not copy arbitrary root files).
// The file is registered in checksums.txt and the aggregate checksum in
// zarf.yaml is updated so the package still passes integrity validation.
// Note: this breaks the signature of signed packages (zarf.yaml is signed)
// — inject before signing, or re-sign afterwards.
// Usage: go run ./tools/addschema <pkg.tar.zst> <schema.json> <out.tar.zst>
package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"gopkg.in/yaml.v3"
)

type entry struct {
	hdr  *tar.Header
	data []byte
}

func main() {
	src, schemaPath, dst := os.Args[1], os.Args[2], os.Args[3]

	in, err := os.Open(src)
	must(err)
	defer in.Close()
	zr, err := zstd.NewReader(in)
	must(err)
	defer zr.Close()

	var entries []entry
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(err)
		data, err := io.ReadAll(tr)
		must(err)
		entries = append(entries, entry{hdr, data})
	}

	schema, err := os.ReadFile(schemaPath)
	must(err)

	// 1. register config.schema.json in checksums.txt
	var checksums string
	for _, e := range entries {
		if e.hdr.Name == "checksums.txt" {
			checksums = string(e.data)
		}
	}
	sum := sha256.Sum256(schema)
	lines := []string{}
	for _, l := range strings.Split(checksums, "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	lines = append(lines, fmt.Sprintf("%s %s", hex.EncodeToString(sum[:]), "config.schema.json"))
	slices.Sort(lines)
	newChecksums := strings.Join(lines, "\n") + "\n"

	// 2. update metadata.aggregateChecksum in zarf.yaml
	agg := sha256.Sum256([]byte(newChecksums))
	for i, e := range entries {
		if e.hdr.Name != "zarf.yaml" {
			continue
		}
		var doc map[string]any
		must(yaml.Unmarshal(e.data, &doc))
		meta, ok := doc["metadata"].(map[string]any)
		if !ok {
			panic("zarf.yaml has no metadata")
		}
		meta["aggregateChecksum"] = hex.EncodeToString(agg[:])
		out, err := yaml.Marshal(doc)
		must(err)
		entries[i].data = out
		entries[i].hdr.Size = int64(len(out))
	}

	// 3. write the new tarball
	out, err := os.Create(dst)
	must(err)
	defer out.Close()
	zw, err := zstd.NewWriter(out, zstd.WithEncoderLevel(zstd.SpeedDefault))
	must(err)
	defer zw.Close()
	tw := tar.NewWriter(zw)

	must(tw.WriteHeader(&tar.Header{
		Name:    "config.schema.json",
		Mode:    0o644,
		Size:    int64(len(schema)),
		ModTime: time.Now(),
	}))
	_, err = tw.Write(schema)
	must(err)

	for _, e := range entries {
		if e.hdr.Name == "checksums.txt" {
			e.data = []byte(newChecksums)
			e.hdr.Size = int64(len(e.data))
		}
		e.hdr.ModTime = time.Now()
		must(tw.WriteHeader(e.hdr))
		_, err = io.Copy(tw, bytes.NewReader(e.data))
		must(err)
	}
	must(tw.Close())
	must(zw.Close())
	fmt.Println("wrote", dst)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

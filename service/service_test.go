package service

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pierrec/lz4/v4"

	"github.com/sinbad/lfs-folderstore/api"
	"github.com/stretchr/testify/assert"
)

func TestStoragePath(t *testing.T) {
	type args struct {
		baseDir string
		oid     string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		// platform-specific tests still run but use filepath.Join to make consistent
		{
			name: "Windows drive",
			args: args{baseDir: `C:/Storage/Dir`, oid: "123456789abcdef"},
			want: filepath.Join(`C:/Storage/Dir`, "12", "34", "123456789abcdef"),
		},
		{
			name: "Windows drive with space",
			args: args{baseDir: `C:/Storage Path/Dir`, oid: "123456789abcdef"},
			want: filepath.Join(`C:/Storage Path/Dir`, "12", "34", "123456789abcdef"),
		},
		{
			name: "Windows share",
			args: args{baseDir: `\\MyServer\Storage Path\Dir`, oid: "123456789abcdef"},
			want: filepath.Join(`\\MyServer\Storage Path\Dir`, "12", "34", "123456789abcdef"),
		},
		{
			name: "Windows trailing separator",
			args: args{baseDir: `C:/Storage/Dir/`, oid: "123456789abcdef"},
			want: filepath.Join(`C:/Storage/Dir`, "12", "34", "123456789abcdef"),
		},
		{
			name: "Unix path",
			args: args{baseDir: `/home/bob/`, oid: "123456789abcdef"},
			want: filepath.Join(`/home/bob`, "12", "34", "123456789abcdef"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := storagePath(tt.args.baseDir, tt.args.oid); got != tt.want {
				t.Errorf("storagePath() = %v, want %v", got, tt.want)
			}
		})
	}
}

func addUpload(t *testing.T, buf *bytes.Buffer, path, oid string, size int64) {
	req := &api.Request{
		Event:  "upload",
		Oid:    oid,
		Size:   size,
		Path:   path,
		Action: &api.Action{},
	}
	b, err := json.Marshal(req)
	assert.Nil(t, err)
	b = append(b, '\n')

	buf.Write(b)
}

func initUpload(buf *bytes.Buffer) {
	buf.WriteString(`{ "event": "init", "operation": "upload", "remote": "origin", "concurrent": true, "concurrenttransfers": 3 }`)
	buf.WriteString("\n")
}

func finishUpload(buf *bytes.Buffer) {
	buf.WriteString(`{ "event": "terminate" }`)
	buf.WriteString("\n")
}

func TestUpload(t *testing.T) {

	setup := setupUploadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	// Perform entire sequence
	Serve(setup.remotepath, setup.remotepath, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	// Check reported progress and completion
	stdoutStr := stdout.String()
	// init report
	assert.Contains(t, stdoutStr, "{}")
	// progress & completion for each file (only 2 uploaded)
	for _, file := range setup.files {
		assert.Contains(t, stdoutStr, `{"event":"progress","oid":"`+file.oid)
		assert.Contains(t, stdoutStr, `{"event":"complete","oid":"`+file.oid)
	}

	// Check actual files are there
	for _, file := range setup.files {
		expectedPath := filepath.Join(setup.remotepath, file.oid[0:2], file.oid[2:4], file.oid)
		assert.FileExistsf(t, expectedPath, "Store file must exist: %v", expectedPath)

		// Check size of file
		s, _ := os.Stat(expectedPath)
		assert.Equal(t, file.size, s.Size())

		// Re-calculate hash to verify
		oid := calculateFileHash(t, expectedPath)
		assert.Equal(t, file.oid, oid)
	}

	// Now try to perform an upload with files 3 & 4 - only one is new
	setup2 := setupUploadTest2(t, setup.localpath, setup.remotepath)
	stdout.Reset()
	stderr.Reset()
	Serve(setup2.remotepath, setup2.remotepath, false, false, bytes.NewReader(setup2.inputBuffer.Bytes()), &stdout, &stderr)

	stdoutStr = stdout.String()
	stderrStr := stderr.String()

	// First file should not be updated (is file3 in original)
	assert.Contains(t, stderrStr, "Skipping "+setup2.files[0].oid)

	// Make sure second file was uploaded
	for _, file := range setup2.files {
		assert.Contains(t, stdoutStr, `{"event":"progress","oid":"`+file.oid)
		assert.Contains(t, stdoutStr, `{"event":"complete","oid":"`+file.oid)
	}

	// Check actual files are there
	for _, file := range setup2.files {
		expectedPath := filepath.Join(setup.remotepath, file.oid[0:2], file.oid[2:4], file.oid)
		assert.FileExistsf(t, expectedPath, "Store file must exist: %v", expectedPath)

		// Check size of file
		s, _ := os.Stat(expectedPath)
		assert.Equal(t, file.size, s.Size())

		// Re-calculate hash to verify
		oid := calculateFileHash(t, expectedPath)
		assert.Equal(t, file.oid, oid)
	}

}

func TestUploadRclone(t *testing.T) {

	setup := setupUploadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	scriptDir, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-rclone")
	assert.Nil(t, err)
	defer os.RemoveAll(scriptDir)

	scriptPath := filepath.Join(scriptDir, "rclone")
	scriptContent := "#!/bin/sh\nif [ \"$1\" = \"copyto\" ]; then\n  src=\"$2\"\n  dest=${3#*:}\n  mkdir -p \"$(dirname \"$dest\")\"\n  cp \"$src\" \"$dest\"\nelif [ \"$1\" = \"lsjson\" ]; then\n  p=${2#*:}\n  if [ -f \"$p\" ]; then\n    size=$(stat -c %s \"$p\")\n    printf '[{\"Name\":\"%s\",\"Size\":%s}]\\n' \"$(basename \"$p\")\" \"$size\"\n  else\n    exit 1\n  fi\nfi\n"
	assert.Nil(t, ioutil.WriteFile(scriptPath, []byte(scriptContent), 0755))

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", scriptDir+string(os.PathListSeparator)+origPath)
	defer os.Setenv("PATH", origPath)

	base := "dummy:" + setup.remotepath

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	Serve(base, base, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	stdoutStr := stdout.String()
	for _, file := range setup.files {
		assert.Contains(t, stdoutStr, `{"event":"progress","oid":"`+file.oid)
		assert.Contains(t, stdoutStr, `{"event":"complete","oid":"`+file.oid)

		expectedPath := filepath.Join(setup.remotepath, file.oid[0:2], file.oid[2:4], file.oid)
		assert.FileExistsf(t, expectedPath, "Store file must exist: %v", expectedPath)

		s, _ := os.Stat(expectedPath)
		assert.Equal(t, file.size, s.Size())

		oid := calculateFileHash(t, expectedPath)
		assert.Equal(t, file.oid, oid)
	}
}

type testFile struct {
	path string
	size int64
	oid  string
}
type testSetup struct {
	localpath   string
	remotepath  string
	files       []testFile
	inputBuffer *bytes.Buffer
}

func setupUploadTest(t *testing.T) *testSetup {
	// Create 2 temporary dirs, pretending to be git repo and dest shared folder
	gitpath, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-test-local")
	assert.Nil(t, err, "Error creating temp git path")

	storepath, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-test-remote")
	assert.Nil(t, err, "Error creating temp shared path")

	testfiles := []testFile{
		{ // small file
			path: filepath.Join(gitpath, "file1"),
			size: 650,
		},
		{ // Multiple block file
			path: filepath.Join(gitpath, "file2"),
			size: 4 * 1024 * 16 * 2,
		},
		{ // Multiple block file with remainder
			path: filepath.Join(gitpath, "file3"),
			size: 4*1024*16*6 + 345,
		},
	}

	for i, file := range testfiles {
		// note must reindex since file is by value
		testfiles[i].oid = createTestFile(t, file.size, file.path)
	}

	// Construct an input buffer of commands to upload first 2 files
	var commandBuf bytes.Buffer
	initUpload(&commandBuf)

	for _, file := range testfiles {
		addUpload(t, &commandBuf, file.path, file.oid, file.size)
	}

	finishUpload(&commandBuf)

	return &testSetup{
		localpath:   gitpath,
		remotepath:  storepath,
		files:       testfiles,
		inputBuffer: &commandBuf,
	}

}

func setupUploadTest2(t *testing.T, gitpath, storepath string) *testSetup {

	testfiles := []testFile{
		{ // File 3 again
			path: filepath.Join(gitpath, "file3"),
			size: 4*1024*16*6 + 345,
		},
		{ // File 3 again
			path: filepath.Join(gitpath, "file4"),
			size: 4*1024*16*2 + 1020,
		},
	}

	testfiles[0].oid = calculateFileHash(t, testfiles[0].path)
	testfiles[1].oid = createTestFile(t, testfiles[1].size, testfiles[1].path)

	// Construct an input buffer of commands to upload first 2 files
	var commandBuf bytes.Buffer
	initUpload(&commandBuf)

	for _, file := range testfiles {
		addUpload(t, &commandBuf, file.path, file.oid, file.size)
	}

	finishUpload(&commandBuf)

	return &testSetup{
		localpath:   gitpath,
		remotepath:  storepath,
		files:       testfiles,
		inputBuffer: &commandBuf,
	}

}

func TestDownload(t *testing.T) {
	setup := setupDownloadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	// Perform entire sequence
	Serve(setup.remotepath, setup.remotepath, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	// Check reported progress and completion
	stdoutStr := stdout.String()
	// init report
	assert.Contains(t, stdoutStr, "{}")
	// progress & completion for each file (only 2 Downloaded)
	for _, file := range setup.files {
		assert.Contains(t, stdoutStr, `{"event":"progress","oid":"`+file.oid)
		assert.Contains(t, stdoutStr, `{"event":"complete","oid":"`+file.oid)
	}

	// Check actual files are in the path specified
	// NB: won't be in the local store, because git-lfs moves into that location
	for _, file := range setup.files {
		assert.FileExistsf(t, file.path, "Local file must exist: %v", file.path)

		// Check size of file
		s, _ := os.Stat(file.path)
		assert.Equal(t, file.size, s.Size())

		// Re-calculate hash to verify
		oid := calculateFileHash(t, file.path)
		assert.Equal(t, file.oid, oid)
	}

	// No need to test partial download since git-lfs eliminates those,
	// custom adapter has no way to know what's in the local repo

}

func TestDownloadFallback(t *testing.T) {
	setup := setupDownloadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	emptyDir, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-empty")
	assert.Nil(t, err)
	defer os.RemoveAll(emptyDir)

	base := emptyDir + ";" + setup.remotepath

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	Serve(base, base, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	paths := completionPaths(t, stdout.String())
	for _, file := range setup.files {
		tempPath, ok := paths[file.oid]
		assert.True(t, ok)
		s, _ := os.Stat(tempPath)
		assert.Equal(t, file.size, s.Size())
		oid := calculateFileHash(t, tempPath)
		assert.Equal(t, file.oid, oid)
	}
}

func TestDownloadZip(t *testing.T) {
	setup := setupDownloadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	// convert store files to zip
	for i, file := range setup.files {
		zipPath := file.path + ".zip"
		assert.Nil(t, createZipFromFile(file.path, zipPath))
		os.Remove(file.path)
		setup.files[i].path = zipPath
	}

	emptyDir, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-empty")
	assert.Nil(t, err)
	defer os.RemoveAll(emptyDir)

	base := emptyDir + ";" + setup.remotepath

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	Serve(base, base, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	paths := completionPaths(t, stdout.String())
	for _, file := range setup.files {
		tempPath, ok := paths[file.oid]
		assert.True(t, ok)
		s, _ := os.Stat(tempPath)
		assert.Equal(t, file.size, s.Size())
		oid := calculateFileHash(t, tempPath)
		assert.Equal(t, file.oid, oid)
	}
}

func TestDownloadLz4(t *testing.T) {
	setup := setupDownloadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	for i, file := range setup.files {
		lz4Path := file.path + ".lz4"
		assert.Nil(t, createLz4FromFile(file.path, lz4Path))
		os.Remove(file.path)
		setup.files[i].path = lz4Path
	}

	emptyDir, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-empty")
	assert.Nil(t, err)
	defer os.RemoveAll(emptyDir)

	base := emptyDir + ";" + setup.remotepath

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	Serve(base, base, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	paths := completionPaths(t, stdout.String())
	for _, file := range setup.files {
		tempPath, ok := paths[file.oid]
		assert.True(t, ok)
		s, _ := os.Stat(tempPath)
		assert.Equal(t, file.size, s.Size())
		oid := calculateFileHash(t, tempPath)
		assert.Equal(t, file.oid, oid)
	}
}

func TestDownloadRclone(t *testing.T) {
	setup := setupDownloadTest(t)
	defer os.RemoveAll(setup.localpath)
	defer os.RemoveAll(setup.remotepath)

	scriptDir, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-rclone")
	assert.Nil(t, err)
	defer os.RemoveAll(scriptDir)

	scriptPath := filepath.Join(scriptDir, "rclone")
	scriptContent := "#!/bin/sh\nif [ \"$1\" = \"cat\" ]; then\n  p=${2#*:}\n  cat \"$p\"\nfi\n"
	assert.Nil(t, ioutil.WriteFile(scriptPath, []byte(scriptContent), 0755))

	origPath := os.Getenv("PATH")
	os.Setenv("PATH", scriptDir+string(os.PathListSeparator)+origPath)
	defer os.Setenv("PATH", origPath)

	base := "dummy:" + setup.remotepath

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	Serve(base, base, false, false, bytes.NewReader(setup.inputBuffer.Bytes()), &stdout, &stderr)

	paths := completionPaths(t, stdout.String())
	for _, file := range setup.files {
		tempPath, ok := paths[file.oid]
		assert.True(t, ok)
		s, _ := os.Stat(tempPath)
		assert.Equal(t, file.size, s.Size())
		oid := calculateFileHash(t, tempPath)
		assert.Equal(t, file.oid, oid)
	}
}

func addDownload(t *testing.T, buf *bytes.Buffer, oid string, size int64) {
	req := &api.Request{
		Event:  "download",
		Oid:    oid,
		Size:   size,
		Action: &api.Action{},
	}
	b, err := json.Marshal(req)
	assert.Nil(t, err)
	b = append(b, '\n')

	buf.Write(b)
}

func initDownload(buf *bytes.Buffer) {
	buf.WriteString(`{ "event": "init", "operation": "download", "remote": "origin", "concurrent": true, "concurrenttransfers": 3 }`)
	buf.WriteString("\n")
}

func finishDownload(buf *bytes.Buffer) {
	buf.WriteString(`{ "event": "terminate" }`)
	buf.WriteString("\n")
}

func setupDownloadTest(t *testing.T) *testSetup {
	// Create 2 temporary dirs, pretending to be git repo and dest shared folder
	gitpath, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-test-local")
	assert.Nil(t, err, "Error creating temp git path")

	storepath, err := ioutil.TempDir(os.TempDir(), "lfs-folderstore-test-remote")
	assert.Nil(t, err, "Error creating temp shared path")

	testfiles := []testFile{
		{ // small file
			path: filepath.Join(storepath, "file6"),
			size: 1023,
		},
		{ // Multiple block file
			path: filepath.Join(storepath, "file7"),
			size: 4 * 1024 * 16 * 10,
		},
		{ // Multiple block file with remainder
			path: filepath.Join(storepath, "file8"),
			size: 4*1024*16*12 + 456,
		},
	}

	for i, file := range testfiles {
		oid := createTestFile(t, file.size, file.path)
		// move these to final location
		finalLocation := filepath.Join(storepath, oid[0:2], oid[2:4], oid)
		assert.Nil(t, os.MkdirAll(filepath.Dir(finalLocation), 0755))
		assert.Nil(t, os.Rename(file.path, finalLocation))
		// Must re-index since file is byval
		testfiles[i].path = finalLocation
		testfiles[i].oid = oid
	}

	// Construct an input buffer of commands to upload first 2 files
	var commandBuf bytes.Buffer
	initDownload(&commandBuf)

	for _, file := range testfiles {
		addDownload(t, &commandBuf, file.oid, file.size)
	}

	finishDownload(&commandBuf)

	return &testSetup{
		localpath:   gitpath,
		remotepath:  storepath,
		files:       testfiles,
		inputBuffer: &commandBuf,
	}

}

func createTestFile(t *testing.T, size int64, path string) string {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0644)
	assert.Nil(t, err)
	defer f.Close()

	byteSnippet := []byte{
		1, 2, 3, 5, 7, 11, 13, 17, 19, 23, 29,
		31, 37, 41, 43, 47, 53, 59, 61, 67, 71,
		73, 79, 83, 89, 97, 101, 103, 107, 109, 113,
		127, 131, 137, 139, 149, 151, 157, 163, 167, 173,
		179, 181, 191, 193, 197, 199, 211, 223, 227, 229,
		233, 239, 241, 251,
	}

	oidHash := sha256.New()

	bytesLeft := size
	byteSnippetLen := int64(len(byteSnippet))
	for bytesLeft > 0 {
		c := len(byteSnippet)
		if bytesLeft < byteSnippetLen {
			c = int(bytesLeft)
		}
		_, err = f.Write(byteSnippet[0:c])
		oidHash.Write(byteSnippet[0:c])
		assert.Nil(t, err)
		bytesLeft -= byteSnippetLen
	}

	return hex.EncodeToString(oidHash.Sum(nil))
}

func calculateFileHash(t *testing.T, filepath string) string {
	hasher := sha256.New()
	f, err := os.OpenFile(filepath, os.O_RDONLY, 0644)
	assert.Nil(t, err)
	defer f.Close()
	_, err = io.Copy(hasher, f)
	assert.Nil(t, err)

	return hex.EncodeToString(hasher.Sum(nil))
}

func TestRetrieveScript(t *testing.T) {
	gitDir, err := ioutil.TempDir("", "gitdir")
	assert.Nil(t, err)
	defer os.RemoveAll(gitDir)

	srcDir, err := ioutil.TempDir("", "src")
	assert.Nil(t, err)
	defer os.RemoveAll(srcDir)

	srcFile := filepath.Join(srcDir, "file")
	content := []byte("hello")
	err = ioutil.WriteFile(srcFile, content, 0644)
	assert.Nil(t, err)

	var stdout, stderr bytes.Buffer
	writer := bufio.NewWriter(&stdout)
	errWriter := bufio.NewWriter(&stderr)

	script := fmt.Sprintf("cp %s \"$DEST\"", srcFile)
	oid := "123456"
	err = tryRetrieveScript(script, gitDir, oid, int64(len(content)), writer, errWriter)
	assert.Nil(t, err)

	dest := downloadTempPath(gitDir, oid)
	data, err := ioutil.ReadFile(dest)
	assert.Nil(t, err)
	assert.Equal(t, string(content), string(data))
}

func TestStoreScript(t *testing.T) {
	remoteDir, err := ioutil.TempDir("", "remote")
	assert.Nil(t, err)
	defer os.RemoveAll(remoteDir)

	localDir, err := ioutil.TempDir("", "local")
	assert.Nil(t, err)
	defer os.RemoveAll(localDir)

	fromPath := filepath.Join(localDir, "file")
	content := []byte("world")
	err = ioutil.WriteFile(fromPath, content, 0644)
	assert.Nil(t, err)
	stat, err := os.Stat(fromPath)
	assert.Nil(t, err)

	var stdout, stderr bytes.Buffer
	writer := bufio.NewWriter(&stdout)
	errWriter := bufio.NewWriter(&stderr)

	oid := "abcdef"
	script := fmt.Sprintf("cp \"$FROM\" %s/$OID", remoteDir)
	err = storeUsingScript(script, oid, stat, fromPath, writer, errWriter)
	assert.Nil(t, err)

	dest := filepath.Join(remoteDir, oid)
	data, err := ioutil.ReadFile(dest)
	assert.Nil(t, err)
	assert.Equal(t, string(content), string(data))
}

func completionPaths(t *testing.T, stdout string) map[string]string {
	paths := make(map[string]string)
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	for scanner.Scan() {
		var resp api.TransferResponse
		if err := json.Unmarshal(scanner.Bytes(), &resp); err == nil {
			if resp.Event == "complete" {
				paths[resp.Oid] = resp.Path
			}
		}
	}
	return paths
}

func TestServeHandlesLargeRequests(t *testing.T) {
	padding := strings.Repeat("a", 70*1024)
	req := fmt.Sprintf("{\"event\":\"terminate\",\"padding\":\"%s\"}\n", padding)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	Serve("", "", false, false, strings.NewReader(req), &stdout, &stderr)

	assert.Contains(t, stderr.String(), "Terminating test custom adapter gracefully.")
}

func createZipFromFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	zw := zip.NewWriter(out)
	defer zw.Close()
	w, err := zw.Create(filepath.Base(src))
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, in); err != nil {
		return err
	}
	return nil
}

func createLz4FromFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	w := lz4.NewWriter(out)
	if _, err := io.Copy(w, in); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return nil
}

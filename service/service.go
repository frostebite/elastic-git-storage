package service

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pierrec/lz4/v4"

	"github.com/sinbad/lfs-folderstore/api"
	"github.com/sinbad/lfs-folderstore/util"
)

// Serve starts the protocol server
// usePullAction/usePushAction indicate whether to fall back to LFS actions
// for downloads and uploads respectively.
func Serve(pullBaseDir, pushBaseDir string, usePullAction, usePushAction bool, stdin io.Reader, stdout, stderr io.Writer) {

	scanner := bufio.NewScanner(stdin)
	// Allow requests larger than the default 64 KB limit by raising the
	// maximum token size to 1 MB.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	writer := bufio.NewWriter(stdout)
	errWriter := bufio.NewWriter(stderr)

	gitDir, err := gitDir()
	if err != nil {
		util.WriteToStderr(fmt.Sprintf("Unable to retrieve git dir: %v\n", err), errWriter)
		return
	}

	for scanner.Scan() {
		line := scanner.Text()
		var req api.Request

		if err := json.Unmarshal([]byte(line), &req); err != nil {
			util.WriteToStderr(fmt.Sprintf("Unable to parse request: %v\n", line), errWriter)
			continue
		}

		switch req.Event {
		case "init":
			resp := &api.InitResponse{}
			if len(pullBaseDir) == 0 {
				resp.Error = &api.TransferError{Code: 9, Message: "Base directory not specified, check config"}
			} else {
				util.WriteToStderr(fmt.Sprintf("Initialised lfs-folderstore custom adapter for %s\n", req.Operation), errWriter)
			}
			api.SendResponse(resp, writer, errWriter)
		case "download":
			util.WriteToStderr(fmt.Sprintf("Received download request for %s\n", req.Oid), errWriter)
			retrieve(pullBaseDir, gitDir, req.Oid, req.Size, usePullAction, req.Action, writer, errWriter)
		case "upload":
			util.WriteToStderr(fmt.Sprintf("Received upload request for %s\n", req.Oid), errWriter)
			if len(pushBaseDir) == 0 {
				pushBaseDir = pullBaseDir
			}
			store(pushBaseDir, req.Oid, req.Size, usePushAction, req.Action, req.Path, writer, errWriter)
		case "terminate":
			util.WriteToStderr("Terminating test custom adapter gracefully.\n", errWriter)
			break
		}
	}

}

func storagePath(baseDir string, oid string) string {
	// Use same folder split as lfs itself
	fld := filepath.Join(baseDir, oid[0:2], oid[2:4])
	return filepath.Join(fld, oid)
}

func downloadTempPath(gitDir string, oid string) (string, error) {
	// Download to a subfolder of repo so that git-lfs's final rename can work
	// It won't work if TEMP is on another drive otherwise
	// basedir is the objects/ folder, so use the tmp folder
	tmpfld := filepath.Join(gitDir, "lfs", "tmp")
	if err := os.MkdirAll(tmpfld, os.ModePerm); err != nil {
		return "", err
	}
	return filepath.Join(tmpfld, fmt.Sprintf("%v.tmp", oid)), nil
}

func retrieve(baseDir, gitDir, oid string, size int64, useAction bool, a *api.Action, writer, errWriter *bufio.Writer) {

	dirs := splitBaseDirs(baseDir)
	var lastErr error
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if len(dir) == 0 {
			continue
		}
		var err error
		if strings.HasPrefix(dir, "|") {
			err = tryRetrieveScript(dir[1:], gitDir, oid, size, writer, errWriter)
		} else {
			err = tryRetrieveDir(dir, gitDir, oid, size, writer, errWriter)
		}
		if err == nil {
			return
		}
		lastErr = err
	}

	if useAction && a != nil {
		if err := retrieveFromAction(a, gitDir, oid, size, writer, errWriter); err == nil {
			return
		} else {
			lastErr = err
		}
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("object not found")
	}
	api.SendTransferError(oid, 3, fmt.Sprintf("Unable to retrieve %q: %v", oid, lastErr), writer, errWriter)
}

func splitBaseDirs(baseDir string) []string {
	return strings.Split(baseDir, ";")
}

func tryRetrieveDir(dir, gitDir, oid string, size int64, writer, errWriter *bufio.Writer) error {
	if util.IsRclonePath(dir) {
		return retrieveFromRclone(dir, gitDir, oid, size, writer, errWriter)
	}

	filePath := storagePath(dir, oid)
	if stat, err := os.Stat(filePath); err == nil && stat.Mode().IsRegular() {
		f, err := os.Open(filePath)
		if err != nil {
			return err
		}
		defer f.Close()
		return saveToTempFromReader(f, stat.Size(), gitDir, oid, writer, errWriter)
	}

	if _, err := os.Stat(filePath + ".zip"); err == nil {
		return retrieveFromZip(filePath+".zip", gitDir, oid, size, writer, errWriter)
	}

	if _, err := os.Stat(filePath + ".lz4"); err == nil {
		return retrieveFromLz4(filePath+".lz4", gitDir, oid, size, writer, errWriter)
	}

	return fmt.Errorf("%s not found", filePath)
}

func tryRetrieveScript(script, gitDir, oid string, size int64, writer, errWriter *bufio.Writer) error {
	tempPath, err := downloadTempPath(gitDir, oid)
	if err != nil {
		return err
	}
	env := map[string]string{
		"OID":  oid,
		"DEST": tempPath,
		"SIZE": fmt.Sprintf("%d", size),
	}
	if err := runScript(script, env); err != nil {
		return err
	}
	stat, err := os.Stat(tempPath)
	if err != nil {
		return err
	}
	api.SendProgress(oid, stat.Size(), int(stat.Size()), writer, errWriter)
	complete := &api.TransferResponse{Event: "complete", Oid: oid, Path: tempPath, Error: nil}
	if err := api.SendResponse(complete, writer, errWriter); err != nil {
		util.WriteToStderr(fmt.Sprintf("Unable to send completion message: %v\n", err), errWriter)
	}
	return nil
}

func retrieveFromAction(a *api.Action, gitDir, oid string, size int64, writer, errWriter *bufio.Writer) error {
	req, err := http.NewRequest("GET", a.Href, nil)
	if err != nil {
		return err
	}
	for k, v := range a.Header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http error: %v", resp.Status)
	}
	if size == 0 && resp.ContentLength > 0 {
		size = resp.ContentLength
	}
	return saveToTempFromReader(resp.Body, size, gitDir, oid, writer, errWriter)
}

func saveToTempFromReader(r io.Reader, size int64, gitDir, oid string, writer, errWriter *bufio.Writer) error {

	dlfilename, err := downloadTempPath(gitDir, oid)
	if err != nil {
		return fmt.Errorf("error creating temp dir: %v", err)
	}
	dlFile, err := os.OpenFile(dlfilename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("error creating temp file: %v", err)
	}
	defer dlFile.Close()

	cb := func(totalSize, readSoFar int64, readSinceLast int) error {
		api.SendProgress(oid, readSoFar, readSinceLast, writer, errWriter)
		return nil
	}

	if err := copyReader(size, r, dlFile, cb); err != nil {
		dlFile.Close()
		os.Remove(dlfilename)
		return err
	}

	if err := dlFile.Close(); err != nil {
		os.Remove(dlfilename)
		return err
	}

	complete := &api.TransferResponse{Event: "complete", Oid: oid, Path: dlfilename, Error: nil}
	if err := api.SendResponse(complete, writer, errWriter); err != nil {
		util.WriteToStderr(fmt.Sprintf("Unable to send completion message: %v\n", err), errWriter)
	}
	return nil
}

func copyReader(size int64, src io.Reader, dst *os.File, cb copyCallback) error {
	const blockSize = 4 * 1024 * 16
	buf := make([]byte, blockSize)
	var readSoFar int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			readSoFar += int64(n)
			if cb != nil {
				cb(size, readSoFar, n)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func retrieveFromZip(path, gitDir, oid string, size int64, writer, errWriter *bufio.Writer) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) == 0 {
		return fmt.Errorf("zip file empty")
	}
	zf := zr.File[0]
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if size == 0 {
		size = int64(zf.UncompressedSize64)
	}
	return saveToTempFromReader(rc, size, gitDir, oid, writer, errWriter)
}

func retrieveFromLz4(path, gitDir, oid string, size int64, writer, errWriter *bufio.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lr := lz4.NewReader(f)
	return saveToTempFromReader(lr, size, gitDir, oid, writer, errWriter)
}

func retrieveFromRclone(base, gitDir, oid string, size int64, writer, errWriter *bufio.Writer) error {
	remote := storagePath(base, oid)
	if data, err := catRclone(remote); err == nil {
		return saveToTempFromReader(bytes.NewReader(data), size, gitDir, oid, writer, errWriter)
	}
	if data, err := catRclone(remote + ".lz4"); err == nil {
		lr := lz4.NewReader(bytes.NewReader(data))
		return saveToTempFromReader(lr, size, gitDir, oid, writer, errWriter)
	}
	if data, err := catRclone(remote + ".zip"); err == nil {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return err
		}
		if len(zr.File) == 0 {
			return fmt.Errorf("zip file empty")
		}
		rc, err := zr.File[0].Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		if size == 0 {
			size = int64(zr.File[0].UncompressedSize64)
		}
		return saveToTempFromReader(rc, size, gitDir, oid, writer, errWriter)
	}
	return fmt.Errorf("rclone path not found")
}

func catRclone(remote string) ([]byte, error) {
	cmd := util.NewCmd("rclone", "cat", remote)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type copyCallback func(totalSize int64, readSoFar int64, readSinceLast int) error

func copyFileContents(size int64, src, dst *os.File, cb copyCallback) error {
	// copy file in chunks (4K is usual block size of disks)
	const blockSize int64 = 4 * 1024 * 16

	// Read precisely the correct number of bytes
	bytesLeft := size
	for bytesLeft > 0 {
		nextBlock := blockSize
		if nextBlock > bytesLeft {
			nextBlock = bytesLeft
		}
		n, err := io.CopyN(dst, src, nextBlock)
		bytesLeft -= n
		if err != nil && err != io.EOF {
			return err
		}
		readSoFar := size - bytesLeft
		if cb != nil {
			cb(size, readSoFar, int(n))
		}
	}
	return nil
}

func store(baseDir string, oid string, size int64, useAction bool, a *api.Action, fromPath string, writer, errWriter *bufio.Writer) {
	statFrom, err := os.Stat(fromPath)
	if err != nil {
		api.SendTransferError(oid, 13, fmt.Sprintf("Cannot stat %q: %v", fromPath, err), writer, errWriter)
		return
	}

	if useAction && a != nil {
		if err := uploadViaAction(a, fromPath, statFrom.Size()); err != nil {
			api.SendTransferError(oid, 21, fmt.Sprintf("Error uploading %q via action: %v", oid, err), writer, errWriter)
			return
		}
	}

	dirs := splitBaseDirs(baseDir)
	var lastErr error
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if len(dir) == 0 {
			continue
		}
		var err error
		if strings.HasPrefix(dir, "|") {
			err = storeUsingScript(dir[1:], oid, statFrom, fromPath, writer, errWriter)
		} else {
			err = storeToDir(dir, oid, statFrom, fromPath, writer, errWriter)
		}
		if err == nil {
			return
		}
		lastErr = err
	}
	api.SendTransferError(oid, 20, fmt.Sprintf("Unable to store %q: %v", oid, lastErr), writer, errWriter)
}

func storeUsingScript(script string, oid string, statFrom os.FileInfo, fromPath string, writer, errWriter *bufio.Writer) error {
	env := map[string]string{
		"OID":  oid,
		"FROM": fromPath,
		"SIZE": fmt.Sprintf("%d", statFrom.Size()),
	}
	if err := runScript(script, env); err != nil {
		return err
	}
	api.SendProgress(oid, statFrom.Size(), int(statFrom.Size()), writer, errWriter)
	complete := &api.TransferResponse{Event: "complete", Oid: oid, Error: nil}
	if err := api.SendResponse(complete, writer, errWriter); err != nil {
		util.WriteToStderr(fmt.Sprintf("Unable to send completion message: %v\n", err), errWriter)
	}
	return nil
}

func storeToDir(baseDir string, oid string, statFrom os.FileInfo, fromPath string, writer, errWriter *bufio.Writer) error {
	destPath := storagePath(baseDir, oid)
	if util.IsRclonePath(baseDir) {
		already, err := storeToRclone(destPath, statFrom, fromPath, oid)
		if err != nil {
			return fmt.Errorf("error uploading %q via rclone: %v", oid, err)
		}
		if already {
			util.WriteToStderr(fmt.Sprintf("Skipping %v, already stored", oid), errWriter)
		}
		api.SendProgress(oid, statFrom.Size(), int(statFrom.Size()), writer, errWriter)
		complete := &api.TransferResponse{Event: "complete", Oid: oid, Error: nil}
		if err := api.SendResponse(complete, writer, errWriter); err != nil {
			util.WriteToStderr(fmt.Sprintf("Unable to send completion message: %v\n", err), errWriter)
		}
		return nil
	}

	statDest, err := os.Stat(destPath)
	if err == nil && statFrom.Size() == statDest.Size() {
		util.WriteToStderr(fmt.Sprintf("Skipping %v, already stored", oid), errWriter)
		api.SendProgress(oid, statFrom.Size(), int(statFrom.Size()), writer, errWriter)
		complete := &api.TransferResponse{Event: "complete", Oid: oid, Error: nil}
		if err := api.SendResponse(complete, writer, errWriter); err != nil {
			util.WriteToStderr(fmt.Sprintf("Unable to send completion message: %v\n", err), errWriter)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("Cannot create dir %q: %v", filepath.Dir(destPath), err)
	}

	tempPath := fmt.Sprintf("%v.tmp", destPath)
	if _, err := os.Stat(tempPath); err == nil {
		if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("Cannot remove existing temp file %q: %v", tempPath, err)
		}
	}

	srcf, err := os.OpenFile(fromPath, os.O_RDONLY, 0644)
	if err != nil {
		return fmt.Errorf("Cannot read data from %q: %v", fromPath, err)
	}
	defer srcf.Close()

	dstf, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, statFrom.Mode())
	if err != nil {
		return fmt.Errorf("Cannot open temp file for writing %q: %v", tempPath, err)
	}

	cb := func(totalSize, readSoFar int64, readSinceLast int) error {
		api.SendProgress(oid, readSoFar, readSinceLast, writer, errWriter)
		return nil
	}

	if err := copyFileContents(statFrom.Size(), srcf, dstf, cb); err != nil {
		dstf.Close()
		os.Remove(tempPath)
		return fmt.Errorf("Error writing temp file %q: %v", tempPath, err)
	}

	dstf.Close()
	if err := os.Rename(tempPath, destPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("Error moving temp file to final location: %v", err)
	}

	complete := &api.TransferResponse{Event: "complete", Oid: oid, Error: nil}
	if err := api.SendResponse(complete, writer, errWriter); err != nil {
		util.WriteToStderr(fmt.Sprintf("Unable to send completion message: %v\n", err), errWriter)
	}
	return nil
}

func uploadViaAction(a *api.Action, fromPath string, size int64) error {
	f, err := os.Open(fromPath)
	if err != nil {
		return err
	}
	defer f.Close()

	req, err := http.NewRequest("PUT", a.Href, f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	for k, v := range a.Header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http error: %v", resp.Status)
	}
	return nil
}

func storeToRclone(destPath string, statFrom os.FileInfo, fromPath, oid string) (bool, error) {
	if size, err := statRclone(destPath); err == nil {
		if size == statFrom.Size() {
			return true, nil
		}
	}

	cmd := util.NewCmd("rclone", "copyto", fromPath, destPath)
	if err := cmd.Run(); err != nil {
		return false, err
	}
	return false, nil
}

func statRclone(remote string) (int64, error) {
	cmd := util.NewCmd("rclone", "lsjson", remote)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, err
	}
	var entries []struct {
		Size int64 `json:"Size"`
	}
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		return 0, err
	}
	if len(entries) == 0 {
		return 0, fmt.Errorf("file not found")
	}
	return entries[0].Size, nil
}

func runScript(script string, env map[string]string) error {
	cmd := util.NewCmd("sh", "-c", script)
	if runtime.GOOS == "windows" {
		cmd = util.NewCmd("cmd", "/C", script)
	}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	return cmd.Run()
}

func gitDir() (string, error) {
	cmd := util.NewCmd("git", "rev-parse", "--git-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("Failed to call git rev-parse --git-dir: %v %v", err, string(out))
	}
	path := strings.TrimSpace(string(out))
	return absPath(path)

}

func absPath(path string) (string, error) {
	if len(path) > 0 {
		path, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(path)
	}
	return "", nil
}

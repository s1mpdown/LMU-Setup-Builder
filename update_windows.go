package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// downloadUpdate fetches the release exe into the settings folder's
// update subfolder (never over the running exe).
func downloadUpdate(assetURL string) (string, error) {
	dir := filepath.Join(configDir(), "update")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, updateAssetName)
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "LMU-Setup-Builder/"+AppVersion)
	resp, err := httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download answered %s", resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", err
	}
	return dst, nil
}

// launchUpdater writes a tiny batch script that waits for this process
// to exit, swaps the downloaded exe over the running one, and relaunches
// it. The caller must exit the app right after this returns nil.
func launchUpdater(newExe string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	bat := filepath.Join(configDir(), "apply-update.bat")
	script := "@echo off\r\n" +
		":retry\r\n" +
		"copy /y \"" + newExe + "\" \"" + self + "\" >nul 2>&1\r\n" +
		"if errorlevel 1 (\r\n" +
		"  timeout /t 1 /nobreak >nul\r\n" +
		"  goto retry\r\n" +
		")\r\n" +
		"start \"\" \"" + self + "\"\r\n" +
		"del \"%~f0\"\r\n"
	if err := os.WriteFile(bat, []byte(script), 0644); err != nil {
		return err
	}
	cmd := exec.Command("cmd.exe", "/C", bat)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000 | 0x00000008} // CREATE_NO_WINDOW | DETACHED_PROCESS
	return cmd.Start()
}

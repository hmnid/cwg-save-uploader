package windows

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	win "golang.org/x/sys/windows"
)

func DataFolder() (string, error) {
	folder, err := win.KnownFolderPath(win.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(folder, "CWG2Uploader"), nil
}

func DefaultSaveFolder() (string, error) {
	folder, err := win.KnownFolderPath(win.FOLDERID_Documents, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(folder, "Warcraft III", "CustomMapData", "CWG2"), nil
}

// PickFolder uses Windows PowerShell's built-in Forms dialog; no external runtime install.
func PickFolder() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	script := `Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description = 'Choose your CWG2 save folder'; $d.ShowNewFolderButton = $false; if ($d.ShowDialog() -eq 'OK') { [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding; [Console]::Write($d.SelectedPath) }; $d.Dispose()`
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-STA", "-Command", script)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := command.Output()
	if err != nil {
		return "", errors.New("could not open folder picker")
	}
	return strings.TrimSpace(strings.TrimPrefix(string(output), "\xef\xbb\xbf")), nil
}

func Open(target string) error {
	operation, _ := win.UTF16PtrFromString("open")
	path, err := win.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return win.ShellExecute(0, operation, path, nil, nil, win.SW_SHOWNORMAL)
}

func Message(title, message string) {
	titlePtr, _ := win.UTF16PtrFromString(title)
	messagePtr, _ := win.UTF16PtrFromString(message)
	box := win.NewLazySystemDLL("user32.dll").NewProc("MessageBoxW")
	box.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), 0x40)
}

func SingleInstance() (func(), error) {
	name, _ := win.UTF16PtrFromString(`Local\CWG2Uploader`)
	handle, err := win.CreateMutex(nil, false, name)
	if err != nil {
		if handle != 0 {
			win.CloseHandle(handle)
		}
		return nil, errors.New("CWG2 Uploader is already running, or its instance lock is unavailable")
	}
	return func() { win.CloseHandle(handle) }, nil
}

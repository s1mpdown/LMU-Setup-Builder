package main

import (
	"syscall"
	"unsafe"
)

// Native Windows folder picker (SHBrowseForFolderW) — walk has no folder
// dialog, and its file dialog can't select an empty folder. The
// BIF_NEWDIALOGSTYLE flag gives a resizable dialog with a New Folder button.

var (
	shell32DLL        = syscall.NewLazyDLL("shell32.dll")
	ole32DLL          = syscall.NewLazyDLL("ole32.dll")
	procSHBrowse      = shell32DLL.NewProc("SHBrowseForFolderW")
	procSHGetPath     = shell32DLL.NewProc("SHGetPathFromIDListW")
	procCoTaskMemFree = ole32DLL.NewProc("CoTaskMemFree")
)

type browseInfoW struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName uintptr
	lpszTitle      uintptr
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
	_              [4]byte
}

// pickFolderDialog shows the folder tree picker. Returns "" on cancel/error.
func pickFolderDialog(owner uintptr, title string) string {
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return ""
	}
	var dispName [260]uint16
	bi := browseInfoW{
		hwndOwner:      owner,
		pszDisplayName: uintptr(unsafe.Pointer(&dispName[0])),
		lpszTitle:      uintptr(unsafe.Pointer(titlePtr)),
		ulFlags:        0x0001 | 0x0040, // BIF_RETURNONLYFSDIRS | BIF_NEWDIALOGSTYLE
	}
	pidl, _, _ := procSHBrowse.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)
	var buf [260]uint16
	ok, _, _ := procSHGetPath.Call(pidl, uintptr(unsafe.Pointer(&buf[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:])
}

//go:build windows

package session

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/sneat-dev/wb/internal/filewrite"
	"golang.org/x/sys/windows"
)

// All lifecycle IO is relative to the retained parent HANDLE, including retry
// reads. No Windows path-based Openat/Renameat compatibility shim is used here.
func registryWindowsName(name string) error {
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\:\x00") {
		return fmt.Errorf("registry lifecycle requires one plain basename")
	}
	if strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return fmt.Errorf("registry lifecycle basename has ambiguous suffix")
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return fmt.Errorf("registry lifecycle basename contains control text")
		}
	}
	base, _, _ := strings.Cut(strings.ToUpper(name), ".")
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && len([]rune(base)) == 4 && strings.ContainsRune("123456789¹²³", []rune(base)[3]) {
		return fmt.Errorf("registry lifecycle basename is a reserved device")
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil || len(encoded) > 256 {
		return fmt.Errorf("registry lifecycle basename exceeds native limit")
	}
	return nil
}

func registryPrivateDescriptor(directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid.String()
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	return windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;" + inherit + ";FA;;;" + sid + ")")
}

func openRegistryWindowsRelative(parent *os.File, name string, access, disposition, options uint32, descriptor *windows.SECURITY_DESCRIPTOR) (*os.File, error) {
	if err := registryWindowsName(name); err != nil {
		return nil, err
	}
	nativeName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: nativeName, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	var status windows.IO_STATUS_BLOCK
	var handle windows.Handle
	var pins runtime.Pinner
	pins.Pin(&attributes)
	pins.Pin(nativeName)
	pins.Pin(nativeName.Buffer)
	if descriptor != nil {
		pins.Pin(descriptor)
	}
	defer pins.Unpin()
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE, &attributes, &status, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition, options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	runtime.KeepAlive(parent)
	runtime.KeepAlive(descriptor)
	runtime.KeepAlive(nativeName)
	if err != nil {
		var status windows.NTStatus
		if errors.As(err, &status) {
			return nil, status.Errno()
		}
		return nil, err
	}
	return os.NewFile(uintptr(handle), name), nil
}

func openRegistryLifecycleDirectory(parent *os.File, create bool) (*os.File, error) {
	disposition := uint32(windows.FILE_OPEN)
	var descriptor *windows.SECURITY_DESCRIPTOR
	if create {
		var err error
		descriptor, err = registryPrivateDescriptor(true)
		if err != nil {
			return nil, err
		}
		disposition = windows.FILE_OPEN_IF
	}
	file, err := openRegistryWindowsRelative(parent, "lifecycle", windows.GENERIC_READ|windows.FILE_TRAVERSE, disposition, windows.FILE_DIRECTORY_FILE, descriptor)
	if err != nil {
		return nil, err
	}
	if err := checkRegistryLifecyclePrivacy(file, true); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
func openRegistryLifecycleFile(parent *os.File, name string) (*os.File, error) {
	return openRegistryWindowsRelative(parent, name, windows.GENERIC_READ, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE, nil)
}

func checkRegistryLifecyclePrivacy(file *os.File, directory bool) error {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || !directory && info.NumberOfLinks != 1 {
		return fmt.Errorf("registry lifecycle handle is not one plain private object")
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return fmt.Errorf("registry lifecycle owner is not current user")
	}
	acl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	if acl == nil || acl.AceCount != 1 {
		return fmt.Errorf("registry lifecycle ACL is not owner-only")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(acl, 0, &ace); err != nil {
		return err
	}
	const allAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask != allAccess || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(user.User.Sid) {
		return fmt.Errorf("registry lifecycle ACL grants unexpected access")
	}
	if directory && ace.Header.AceFlags&(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE {
		return fmt.Errorf("registry lifecycle ACL is not inheritable")
	}
	return nil
}

type registryRenameInfo struct {
	Replace uint32
	Root    windows.Handle
	Length  uint32
	Name    [1]uint16
}

func renameRegistryStage(file, parent *os.File, name string) error {
	if err := registryWindowsName(name); err != nil {
		return err
	}
	encoded, err := windows.UTF16FromString(name)
	if err != nil {
		return err
	}
	var header registryRenameInfo
	offset := unsafe.Offsetof(header.Name)
	buffer := make([]byte, int(offset)+2*len(encoded))
	info := (*registryRenameInfo)(unsafe.Pointer(&buffer[0]))
	info.Root, info.Length = windows.Handle(parent.Fd()), uint32(2*(len(encoded)-1))
	copy(unsafe.Slice((*uint16)(unsafe.Pointer(&buffer[offset])), len(encoded)), encoded)
	err = windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileRenameInfo, &buffer[0], uint32(len(buffer)))
	runtime.KeepAlive(parent)
	runtime.KeepAlive(file)
	runtime.KeepAlive(buffer)
	return err
}
func deleteRegistryStage(file *os.File) error {
	deleteOnClose := uint32(1)
	err := windows.SetFileInformationByHandle(windows.Handle(file.Fd()), windows.FileDispositionInfo, (*byte)(unsafe.Pointer(&deleteOnClose)), uint32(unsafe.Sizeof(deleteOnClose)))
	runtime.KeepAlive(file)
	return err
}

type registryPublisherOps struct {
	create     func(*os.File, string) (*os.File, error)
	write      func(*os.File, []byte) error
	sync       func(*os.File) error
	rename     func(*os.File, *os.File, string) error
	close      func(*os.File) error
	parentSync func(*os.File) error
}

func nativeRegistryPublisherOps() registryPublisherOps {
	return registryPublisherOps{
		create: func(parent *os.File, name string) (*os.File, error) {
			descriptor, err := registryPrivateDescriptor(false)
			if err != nil {
				return nil, err
			}
			return openRegistryWindowsRelative(parent, name, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE, windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, descriptor)
		},
		write:      func(file *os.File, raw []byte) error { return filewrite.Write(file, raw, file.Name(), nil) },
		sync:       func(file *os.File) error { return file.Sync() },
		rename:     renameRegistryStage,
		close:      func(file *os.File) error { return file.Close() },
		parentSync: func(parent *os.File) error { return filewrite.SyncDir(parent, nil) },
	}
}
func publishRegistryLifecycle(parent *os.File, name string, raw []byte, inj *filewrite.Injector) error {
	if inj != nil {
		return fmt.Errorf("generic filewrite injection is Unix-only; native registry publication has platform fault ports")
	}
	return publishRegistryLifecycleWithOps(parent, name, raw, nativeRegistryPublisherOps())
}
func publishRegistryLifecycleWithOps(parent *os.File, name string, raw []byte, ops registryPublisherOps) (resultErr error) {
	if err := registryWindowsName(name); err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > 64<<10 {
		return fmt.Errorf("registry lifecycle marker exceeds native bound")
	}
	if err := checkRegistryLifecyclePrivacy(parent, true); err != nil {
		return err
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	stage := ".registry-stage-" + hex.EncodeToString(token[:])
	file, err := ops.create(parent, stage)
	if err != nil {
		return err
	}
	committed, closed := false, false
	defer func() {
		if !committed {
			resultErr = errors.Join(resultErr, deleteRegistryStage(file))
		}
		if !closed {
			if err := file.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
				resultErr = errors.Join(resultErr, err)
			}
		}
	}()
	if err := checkRegistryLifecyclePrivacy(file, false); err != nil {
		return err
	}
	if err := ops.write(file, raw); err != nil {
		return err
	}
	if err := ops.sync(file); err != nil {
		return err
	}
	if err := checkRegistryLifecyclePrivacy(parent, true); err != nil {
		return err
	}
	if err := ops.rename(file, parent, name); err != nil {
		return err
	}
	committed = true // Never set delete-on-close on the published marker.
	if err := ops.close(file); err != nil {
		return err
	}
	closed = true
	return ops.parentSync(parent)
}

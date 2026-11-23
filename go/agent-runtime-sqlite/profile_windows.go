package sqlite

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FlushFileBuffers requires write access; os.Open returns a read-only handle.
func openProfileDirectoryForSync(path string) (*os.File, error) {
	handle, err := openProfileHandle(path, windows.GENERIC_WRITE)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}

func openProfileHandle(path string, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	handle, err := windows.CreateFile(name, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open profile", Path: path, Err: err}
	}
	return handle, nil
}

func validateProfilePermissions(path string, info os.FileInfo) error {
	handle, err := openProfileHandle(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var file windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &file); err != nil {
		return err
	}
	if file.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(file.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != info.IsDir() {
		return fmt.Errorf("%w: unexpected profile path type: %s", ErrInsecurePath, path)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !privateProfileACL(sd, user.User.Sid, info.IsDir()) {
		return fmt.Errorf("%w: profile owner or access rules are not private: %s", ErrInsecurePath, path)
	}
	return nil
}

func trustedProfileSID(sid, user *windows.SID) bool {
	return sid != nil && (sid.Equals(user) || sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

func privateProfileACL(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID, directory bool) bool {
	owner, _, err := sd.Owner()
	if err != nil || !trustedProfileSID(owner, user) {
		return false
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return false
	}
	// A file-inheritable grant also protects SQLite's next WAL/SHM.
	// Without any inheritable ACE, Windows can use the creator's default DACL.
	fileInherits := false
	for i := uint16(0); i < acl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, uint32(i), &ace) != nil {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			creator := ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 && sid.IsWellKnown(windows.WinCreatorOwnerSid)
			if !trustedProfileSID(sid, user) && !sid.IsWellKnown(windows.WinCreatorOwnerRightsSid) && !creator {
				return false
			}
			fileInherits = fileInherits || ace.Header.AceFlags&windows.OBJECT_INHERIT_ACE != 0
		default:
			return false
		}
	}
	return !directory || fileInherits
}
